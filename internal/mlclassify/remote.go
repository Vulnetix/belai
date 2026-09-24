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
// text-classification models, served through the Inference Providers router.
// The legacy api-inference.huggingface.co host no longer resolves, which made
// every remote phase call fail (and so fail closed). It is a different
// endpoint from the registry's chat BaseURL, so the remote gate runs its own
// small client and reuses only the credential lookup.
const hfInferenceEndpoint = "https://router.huggingface.co/hf-inference/models/"

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
	endpoint  string // inference base URL; hfInferenceEndpoint unless a test overrides it
}

func newRemoteGate(phase Phase, mc ModelConfig, hfToken func() (string, error)) (gate, error) {
	// A remote model selected from the curated classifier catalogue can
	// resolve its attack label here, so the caller never has to guess one.
	if mc.AttackLabel == "" {
		if label, ok := AttackLabelFor(mc.ID); ok {
			mc.AttackLabel = label
		}
	}
	if mc.AttackLabel == "" {
		return nil, fmt.Errorf("remote model %q: AttackLabel is required", mc.ID)
	}
	if hfToken == nil {
		hfToken = func() (string, error) { return "", fmt.Errorf("no HuggingFace token resolver") }
	}
	client := &http.Client{Timeout: 30 * time.Second}

	token, err := hfToken()
	if err != nil {
		// A token is optional for public models; the download still works
		// without one, so treat a missing token as empty here and let the
		// inference call surface a 401 if it is actually required.
		token = ""
	}

	// Fetch the tokenizer files once so windowing matches server-side
	// tokenization. Prefer the persistent on-disk cache (populated by a
	// previous extraction or remote fetch) so missing or 404-prone files do
	// not break a model that already has them locally.
	dir, err := modelCacheDir(mc.ID)
	if err != nil {
		return nil, err
	}
	// If this model is embedded and the cache is empty, seed it from the
	// binary so the tokenizer files are available even when the upstream
	// repo does not ship them (e.g. the jailbreak model has no vocab.txt).
	if spec, ok := embeddedSpecFor(mc.ID); ok {
		if _, err := os.Stat(filepath.Join(dir, "spago_model.bin")); err != nil {
			if _, err := extractEmbedded(spec); err != nil {
				// Non-fatal: fall through to remote fetch below.
			}
		}
	}
	for _, f := range []string{"vocab.txt", "tokenizer_config.json"} {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		data, err := fetchHFFile(context.Background(), client, mc.ID, "main", f, token)
		if err != nil {
			return nil, fmt.Errorf("remote model %q: fetch %s: %w", mc.ID, f, err)
		}
		if err := writeFileAtomic(path, data); err != nil {
			return nil, fmt.Errorf("remote model %q: cache %s: %w", mc.ID, f, err)
		}
	}
	tokFn, err := newWordPieceTokenizer(filepath.Join(dir, "vocab.txt"))
	if err != nil {
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
		endpoint:  hfInferenceEndpoint,
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

// hfLabelScore is one label in a text-classification reply.
type hfLabelScore struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

// decodeHFClassification accepts both reply shapes the inference API has
// served for text classification: nested per input,
// [[{"label": "...", "score": 0.9}, ...]], and flat for a single input,
// [{"label": "...", "score": 0.9}, ...]. An empty or unrecognised reply is an
// error, so the gate fails closed.
func decodeHFClassification(data []byte) ([]hfLabelScore, error) {
	var nested [][]hfLabelScore
	if err := json.Unmarshal(data, &nested); err == nil {
		if len(nested) == 0 || len(nested[0]) == 0 {
			return nil, fmt.Errorf("empty response")
		}
		return nested[0], nil
	}
	var flat []hfLabelScore
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(flat) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return flat, nil
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
	endpoint := g.endpoint
	if endpoint == "" {
		endpoint = hfInferenceEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+g.id, bytes.NewReader(body))
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
	labels, err := decodeHFClassification(data)
	if err != nil {
		return "", 0, fmt.Errorf("classify %s: %w", g.id, err)
	}
	score := 0.0
	for _, r := range labels {
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
