package mlclassify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// hfInferenceEndpoint is the HuggingFace serverless inference API for
// text-classification models. It is a different endpoint from the registry's
// chat BaseURL, so the remote gate runs its own small client and reuses only
// the credential lookup.
const hfInferenceEndpoint = "https://api-inference.huggingface.co/models/"

// remoteModel runs one phase model over the HuggingFace inference API.
type remoteModel struct {
	id        string
	ph        Phase
	sentinel  rolemanager.Sentinel
	attack    string // attack label name
	threshold float64
	token     func() (string, error)
	client    *http.Client
	tokFn     tokenizeFunc
}

func newRemoteGate(phase Phase, mc ModelConfig, hfToken func() (string, error)) (gate, error) {
	if mc.AttackLabel == "" {
		return nil, fmt.Errorf("remote model %q: AttackLabel is required", mc.ID)
	}
	if hfToken == nil {
		hfToken = func() (string, error) { return "", fmt.Errorf("no HuggingFace token resolver") }
	}
	client := &http.Client{Timeout: 30 * time.Second}

	// Fetch the tokenizer files once so windowing matches server-side
	// tokenization. They live under the same model repo.
	dir, err := os.MkdirTemp("", "signet-hf-tok-*")
	if err != nil {
		return nil, err
	}
	token, err := hfToken()
	if err != nil {
		// A token is optional for public models; the download still works
		// without one, so treat a missing token as empty here and let the
		// inference call surface a 401 if it is actually required.
		token = ""
	}
	for _, f := range []string{"vocab.txt", "tokenizer_config.json"} {
		data, err := fetchHFFile(context.Background(), client, mc.ID, "main", f, token)
		if err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("remote model %q: fetch %s: %w", mc.ID, f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), data, 0o600); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	tokFn, err := newWordPieceTokenizer(filepath.Join(dir, "vocab.txt"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("remote model %q: %w", mc.ID, err)
	}

	return &remoteModel{
		id:        mc.ID,
		ph:        phase,
		sentinel:  sentinelFor(phase),
		attack:    mc.AttackLabel,
		threshold: mc.effectiveThreshold(phase),
		token:     hfToken,
		client:    client,
		tokFn:     tokFn,
	}, nil
}

// sentinelFor maps a phase to the verdict its gate emits when it fires.
func sentinelFor(phase Phase) rolemanager.Sentinel {
	switch phase {
	case Phase1:
		return rolemanager.SentinelPromptInjection
	default:
		return rolemanager.SentinelJailbreak
	}
}

func (g *remoteModel) phase() Phase { return g.ph }

func (g *remoteModel) tokenizer() tokenizeFunc { return g.tokFn }

// hfClassificationResponse is the text-classification reply shape:
// [[{"label": "...", "score": 0.9}, ...]].
type hfClassificationResponse [][]struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

func (g *remoteModel) fire(ctx context.Context, window string) (rolemanager.Sentinel, float64, error) {
	token, err := g.token()
	if err != nil {
		return "", 0, fmt.Errorf("classify %s: resolve token: %w", g.id, err)
	}
	body, err := json.Marshal(map[string]any{"inputs": window})
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hfInferenceEndpoint+g.id, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("classify %s: %w", g.id, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("classify %s: %s: %s", g.id, resp.Status, strings.TrimSpace(string(data)))
	}
	var out hfClassificationResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, fmt.Errorf("classify %s: decode response: %w", g.id, err)
	}
	if len(out) == 0 {
		return "", 0, fmt.Errorf("classify %s: empty response", g.id)
	}
	score := 0.0
	for _, r := range out[0] {
		if r.Label == g.attack {
			score = r.Score
			break
		}
	}
	if score >= g.threshold {
		return g.sentinel, score, nil
	}
	return rolemanager.SentinelSafe, score, nil
}

// fetchHFFile downloads one file from a HuggingFace repo at a pinned revision.
func fetchHFFile(ctx context.Context, client *http.Client, id, revision, file, token string) ([]byte, error) {
	url := fmt.Sprintf("https://huggingface.co/%s/resolve/%s/%s", id, revision, file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}
