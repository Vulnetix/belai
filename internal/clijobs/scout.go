package clijobs

import (
	"bytes"
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
)

// The scout detector. It turns the three outputs of the scout job (the
// capabilities file, the detected-files listing of `scan --show-detected` and
// the analyze report) into deep signals. It matches fixed lower-case tokens
// against three bounded views of those outputs and emits only members of
// DeepSignals: no output text is kept, and a token it does not know is
// dropped. No model reads any of it.
//
// The views are: the keys of the capabilities file's `repo` map that are true,
// the file paths in the detected listing, and the metric ids of the analyze
// report.
var deepTokens = map[string][]string{
	"crypto":            {"crypto", ".pem", ".crt", ".p12", ".jks", ".keystore", "openssl", "libsodium", "bouncycastle"},
	"ai-sdk":            {"openai", "anthropic", "langchain", "llama", "transformers", ".gguf", ".safetensors", ".onnx", "huggingface"},
	"agent-config":      {"agents.md", ".mcp.json", "mcp.json", "skill.md", ".agents/", "/agents/"},
	"installed-deps":    {"node_modules/", "/vendor/", ".venv/", "site-packages/", "/target/debug/"},
	"release-artifacts": {"goreleaser", ".releaserc", "checksums.txt", "/dist/", ".nupkg", ".whl", "release-please"},
}

const (
	maxViewBytes = 1 << 20
	maxViewItems = 20000
)

// Detect returns the deep signals the three outputs show, sorted.
func Detect(capabilitiesYAML, detectedListing, analyzeJSON []byte) []string {
	var views []string
	views = append(views, capabilityKeys(capabilitiesYAML)...)
	views = append(views, listingPaths(detectedListing)...)
	views = append(views, metricIDs(analyzeJSON)...)
	found := map[string]bool{}
	for _, v := range views {
		v = strings.ToLower(v)
		for sig, tokens := range deepTokens {
			if found[sig] {
				continue
			}
			for _, t := range tokens {
				if strings.Contains(v, t) {
					found[sig] = true
					break
				}
			}
		}
	}
	var out []string
	for sig := range found {
		out = append(out, sig)
	}
	return cleanSignals(out)
}

func capabilityKeys(raw []byte) []string {
	if len(raw) == 0 || len(raw) > maxViewBytes {
		return nil
	}
	var doc struct {
		Repo map[string]bool `yaml:"repo"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var out []string
	for k, v := range doc.Repo {
		if v && len(out) < maxViewItems {
			out = append(out, k)
		}
	}
	return out
}

// listingPaths returns the lines of the detected-files listing as path views.
// A line is used only as a haystack for fixed tokens.
func listingPaths(raw []byte) []string {
	if len(raw) > maxViewBytes {
		raw = raw[len(raw)-maxViewBytes:]
	}
	var out []string
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(out) >= maxViewItems {
			break
		}
		if s := strings.TrimSpace(string(line)); s != "" && len(s) <= 400 {
			out = append(out, s)
		}
	}
	return out
}

func metricIDs(raw []byte) []string {
	if len(raw) == 0 || len(raw) > 4*maxViewBytes {
		return nil
	}
	var doc struct {
		Metrics []struct {
			ID string `json:"id"`
		} `json:"metrics"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	var out []string
	for _, m := range doc.Metrics {
		if len(out) < maxViewItems {
			out = append(out, m.ID)
		}
	}
	return out
}
