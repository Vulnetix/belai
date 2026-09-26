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

	"github.com/vulnetix/belai/internal/rolemanager"
)

// hfInferenceEndpoint is the HuggingFace serverless inference API for
// text-classification models, served through the Inference Providers router.
// The legacy api-inference.huggingface.co host no longer resolves, which made
// every remote phase call fail (and so fail closed). It is a different
// endpoint from the registry's chat BaseURL, so the remote gate runs its own
// small client and reuses only the credential lookup.
const hfInferenceEndpoint = "https://router.huggingface.co/hf-inference/models/"

// hubModelsAPI is the Hub API endpoint that reports a model repo's file list.
// It is a package variable so tests can point it at a stub server.
var hubModelsAPI = "https://huggingface.co/api/models/"

// hubFilesTTL bounds how stale the cached repo file list may be before the
// gate re-checks it. The list only feeds the servability verdict, so a week
// of staleness is fine and keeps per-turn pipeline rebuilds off the Hub API.
const hubFilesTTL = 7 * 24 * time.Hour

// hubFileCacheFile is the per-model marker recording the last fetched file
// list of the model's HuggingFace repo.
const hubFileCacheFile = ".hub_files.json"

// tokenizerFileNames are the repo files that let the HuggingFace inference
// server load a model's tokenizer. A repo that ships none of them cannot be
// loaded by HF serverless inference at all: the API 400s with "Can't load
// tokenizer for '/repository'" (the server-side mount path of the repo, not a
// path on the caller's machine). The known phase-1 saturation model is
// exactly such a repo, which made the remote phase-1 default fail every
// prompt.
var tokenizerFileNames = []string{"tokenizer.json", "vocab.txt", "vocab.json"}

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

	// The on-disk cache directory for this model. The servability check caches
	// its verdict there, and the tokenizer fetches below fall back to it.
	dir, err := modelCacheDir(mc.ID)
	if err != nil {
		return nil, err
	}

	// Fail fast, before any tokenizer fetch or per-prompt call, when the
	// HuggingFace inference API cannot serve the model at all. HF serverless
	// inference mounts the repo at its own "/repository" path and loads the
	// tokenizer from it, so a repo that ships no tokenizer files 400s on
	// every call — including the known phase-1 model. The gate refuses such a
	// model here with an actionable error instead of dying per prompt.
	if tokenizable, err := hfRepoTokenizable(client, dir, mc.ID, token); err != nil {
		return nil, fmt.Errorf("remote model %q: %w", mc.ID, err)
	} else if !tokenizable {
		return nil, fmt.Errorf("remote model %q: HuggingFace serverless inference cannot serve it — the repo ships no tokenizer files (tokenizer.json/vocab.txt), so the inference API fails to load the model. Run it locally instead: build belai with the embedded model (just build-bert or just build-jailbreak)", mc.ID)
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
		msg := strings.TrimSpace(string(data))
		if strings.Contains(msg, "Model not supported by provider") {
			return "", 0, fmt.Errorf("classify %s: HuggingFace serverless inference does not serve this model (%s); run it locally instead — build belai with the embedded model (just build-bert or just build-jailbreak)", g.id, msg)
		}
		return "", 0, fmt.Errorf("classify %s: %s: %s", g.id, resp.Status, msg)
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

// hubFilesMarker is the on-disk record of the last fetched file list of a
// model's HuggingFace repo.
type hubFilesMarker struct {
	Fetched int64    `json:"fetched"`
	Files   []string `json:"files"`
}

// hasTokenizerFile reports whether a repo file list contains a file the
// HuggingFace inference server can load a tokenizer from.
func hasTokenizerFile(files []string) bool {
	for _, f := range files {
		for _, want := range tokenizerFileNames {
			if f == want {
				return true
			}
		}
	}
	return false
}

// hfRepoTokenizable reports whether the HuggingFace repo for id ships
// tokenizer files, i.e. whether HF serverless inference can load the model
// at all. The verdict is cached on disk in the model cache directory with a
// hubFilesTTL so per-turn pipeline rebuilds do not hit the Hub API. When the
// Hub is unreachable a stale marker degrades to the last known verdict
// instead of failing the check; with no marker at all the check fails closed.
func hfRepoTokenizable(client *http.Client, dir, id, token string) (bool, error) {
	markerPath := filepath.Join(dir, hubFileCacheFile)
	readMarker := func() (hubFilesMarker, bool) {
		data, err := os.ReadFile(markerPath)
		if err != nil {
			return hubFilesMarker{}, false
		}
		var m hubFilesMarker
		if json.Unmarshal(data, &m) != nil {
			return hubFilesMarker{}, false
		}
		return m, true
	}
	if m, ok := readMarker(); ok && time.Since(time.Unix(m.Fetched, 0)) < hubFilesTTL {
		return hasTokenizerFile(m.Files), nil
	}

	req, err := http.NewRequest(http.MethodGet, hubModelsAPI+id, nil)
	if err != nil {
		return false, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusNotFound:
			return false, fmt.Errorf("model %q not found on HuggingFace", id)
		case resp.StatusCode != http.StatusOK:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			err = fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
		default:
			var meta struct {
				Siblings []struct {
					RFilename string `json:"rfilename"`
				} `json:"siblings"`
			}
			if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
				err = fmt.Errorf("decode model metadata: %w", err)
			} else {
				files := make([]string, 0, len(meta.Siblings))
				for _, s := range meta.Siblings {
					files = append(files, s.RFilename)
				}
				marker, _ := json.Marshal(hubFilesMarker{Fetched: time.Now().Unix(), Files: files})
				if werr := writeFileAtomic(markerPath, marker); werr != nil {
					// A marker write failure is not fatal: the verdict is still
					// valid for this check, the next one just re-fetches.
				}
				return hasTokenizerFile(files), nil
			}
		}
	}
	// The Hub check failed (transport, non-200, or bad metadata). A stale
	// marker degrades to the last known verdict; without one the gate fails
	// closed rather than guessing the model is servable.
	if m, ok := readMarker(); ok {
		return hasTokenizerFile(m.Files), nil
	}
	return false, fmt.Errorf("check whether HuggingFace can serve model %q: %w", id, err)
}
