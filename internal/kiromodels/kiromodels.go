// Package kiromodels reads Kiro's live model catalogue (ListAvailableModels)
// and keeps it for the process, so the request builder can send only the
// request fields a model declares.
//
// Kiro validates additionalModelRequestFields against a JSON schema each
// model publishes, and rejects any property the schema does not declare. The
// schema's enclosing key is not documented, so Parse walks each model entry
// for the shapes it needs (an effort enum under output_config or reasoning,
// a max_tokens range) wherever they sit. A model whose schema is missing or
// unreadable gets no extra fields at all: the request fails closed to the
// plain shape rather than to a 400.
//
// The catalogue is harness-computed facts (ids, limits, enums). Names and
// descriptions are reduced to printable text before they are kept.
package kiromodels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/kiroauth"
)

// Effort branches: which additionalModelRequestFields property carries the
// effort level.
const (
	BranchOutputConfig = "output_config" // Claude: output_config.effort
	BranchReasoning    = "reasoning"     // GPT: reasoning.effort
)

// Info is one model the signed-in account can use.
type Info struct {
	ID        string
	Name      string
	Images    bool // supportedInputTypes includes IMAGE
	MaxInput  int
	MaxOutput int
	// EffortBranch is BranchOutputConfig, BranchReasoning, or "" when the
	// model declares no effort.
	EffortBranch string
	Efforts      []string
	// MaxTokensMin/Max bound additionalModelRequestFields.max_tokens; both
	// zero when the schema does not declare it.
	MaxTokensMin, MaxTokensMax int
}

// AcceptsEffort reports whether e is one of the model's declared levels.
func (i Info) AcceptsEffort(e string) bool {
	if i.EffortBranch == "" {
		return false
	}
	for _, x := range i.Efforts {
		if x == e {
			return true
		}
	}
	return false
}

// RequestFields returns the additionalModelRequestFields for effort and a
// completion cap, holding only what the model's schema declares. It returns
// nil when there is nothing to send.
func (i Info) RequestFields(effort string, maxTokens int) map[string]any {
	out := map[string]any{}
	if effort != "" && i.AcceptsEffort(effort) {
		out[i.EffortBranch] = map[string]any{"effort": effort}
	}
	if maxTokens > 0 && i.MaxTokensMax > 0 {
		if maxTokens < i.MaxTokensMin {
			maxTokens = i.MaxTokensMin
		}
		if maxTokens > i.MaxTokensMax {
			maxTokens = i.MaxTokensMax
		}
		out["max_tokens"] = maxTokens
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

const (
	maxBody  = 1 << 20
	maxPages = 5
	// TTL is how long a fetched catalogue is trusted.
	TTL = 5 * time.Minute
)

// Path is the ListAvailableModels operation path.
const Path = "/ListAvailableModels"

// Endpoint is the first-page URL for base and profileARN.
func Endpoint(base, profileARN string) string {
	q := url.Values{"origin": {"AI_EDITOR"}}
	if profileARN != "" {
		q.Set("profileArn", profileARN)
	}
	return strings.TrimRight(base, "/") + Path + "?" + q.Encode()
}

// Fetch lists the models accessToken may use at base (a pinned Kiro API host
// or a loopback mock). Redirects are refused and each page is capped.
func Fetch(ctx context.Context, client *http.Client, base, accessToken, profileARN string) ([]Info, error) {
	base = strings.TrimRight(base, "/")
	if !kiroauth.AllowedAPIURL(base) {
		return nil, fmt.Errorf("refusing to send a Kiro token to %q", base)
	}
	var c http.Client
	if client != nil {
		c = *client
	} else {
		c.Timeout = 30 * time.Second
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	var out []Info
	next := ""
	for page := 0; page < maxPages; page++ {
		u := Endpoint(base, profileARN)
		if next != "" {
			u += "&nextToken=" + url.QueryEscape(next)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list kiro models: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("list kiro models returned HTTP %d", resp.StatusCode)
		}
		infos, tok, err := Parse(data)
		if err != nil {
			return nil, err
		}
		out = append(out, infos...)
		if tok == "" {
			break
		}
		next = tok
	}
	return out, nil
}

// Parse decodes one ListAvailableModels page and returns its models and the
// next-page token.
func Parse(data []byte) ([]Info, string, error) {
	var page struct {
		Models    []json.RawMessage `json:"models"`
		NextToken string            `json:"nextToken"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, "", fmt.Errorf("malformed kiro model list: %w", err)
	}
	out := make([]Info, 0, len(page.Models))
	for _, raw := range page.Models {
		if info, ok := parseModel(raw); ok {
			out = append(out, info)
		}
	}
	return out, page.NextToken, nil
}

func parseModel(raw json.RawMessage) (Info, bool) {
	var m struct {
		ModelID             string   `json:"modelId"`
		ModelName           string   `json:"modelName"`
		SupportedInputTypes []string `json:"supportedInputTypes"`
		TokenLimits         struct {
			MaxInputTokens  int `json:"maxInputTokens"`
			MaxOutputTokens int `json:"maxOutputTokens"`
		} `json:"tokenLimits"`
	}
	if err := json.Unmarshal(raw, &m); err != nil || !validID(m.ModelID) {
		return Info{}, false
	}
	info := Info{
		ID:        m.ModelID,
		Name:      printable(m.ModelName, 80),
		MaxInput:  m.TokenLimits.MaxInputTokens,
		MaxOutput: m.TokenLimits.MaxOutputTokens,
	}
	if info.Name == "" {
		info.Name = info.ID
	}
	for _, t := range m.SupportedInputTypes {
		if strings.EqualFold(t, "IMAGE") {
			info.Images = true
		}
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err == nil {
		walkSchema(tree, &info, 0)
	}
	return info, true
}

// walkSchema looks for a JSON-schema object whose properties declare an
// effort branch or max_tokens, wherever it sits in the model entry. A string
// value that holds a JSON object is decoded and walked too, since a schema
// may travel as text.
func walkSchema(v any, info *Info, depth int) {
	if depth > 12 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		if props, ok := x["properties"].(map[string]any); ok {
			for _, branch := range []string{BranchOutputConfig, BranchReasoning} {
				if info.EffortBranch != "" {
					break
				}
				if enum := effortEnum(props[branch]); len(enum) > 0 {
					info.EffortBranch, info.Efforts = branch, enum
				}
			}
			if mt, ok := props["max_tokens"].(map[string]any); ok && info.MaxTokensMax == 0 {
				lo, _ := mt["minimum"].(float64)
				hi, _ := mt["maximum"].(float64)
				if hi > 0 && lo <= hi {
					info.MaxTokensMin, info.MaxTokensMax = int(lo), int(hi)
				}
			}
		}
		for _, child := range x {
			walkSchema(child, info, depth+1)
		}
	case []any:
		for _, child := range x {
			walkSchema(child, info, depth+1)
		}
	case string:
		s := strings.TrimSpace(x)
		if strings.HasPrefix(s, "{") && len(s) < maxBody {
			var inner any
			if json.Unmarshal([]byte(s), &inner) == nil {
				walkSchema(inner, info, depth+1)
			}
		}
	}
}

// effortEnum returns branch.properties.effort.enum as identifier strings.
func effortEnum(branch any) []string {
	b, ok := branch.(map[string]any)
	if !ok {
		return nil
	}
	props, ok := b["properties"].(map[string]any)
	if !ok {
		return nil
	}
	effort, ok := props["effort"].(map[string]any)
	if !ok {
		return nil
	}
	list, ok := effort["enum"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range list {
		if s, ok := e.(string); ok && validID(s) && len(out) < 16 {
			out = append(out, s)
		}
	}
	return out
}

// validID accepts model ids and enum values: short, and only
// [A-Za-z0-9._:/-].
func validID(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == ':' || r == '/':
		default:
			return false
		}
	}
	return true
}

func printable(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || (r >= 0x200e && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			continue
		}
		b.WriteRune(r)
		if n++; n >= max {
			break
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Process cache
// ---------------------------------------------------------------------------

type entry struct {
	infos []Info
	at    time.Time
}

var (
	mu    sync.Mutex
	cache = map[string]entry{}
	now   = time.Now
)

// Remember keeps a fetched catalogue for base.
func Remember(base string, infos []Info) {
	mu.Lock()
	defer mu.Unlock()
	cache[strings.TrimRight(base, "/")] = entry{infos: infos, at: now()}
}

// Lookup returns the remembered info for model at base. fresh is false when
// base has no catalogue younger than TTL, in which case the caller may fetch.
func Lookup(base, model string) (info Info, found, fresh bool) {
	mu.Lock()
	defer mu.Unlock()
	e, ok := cache[strings.TrimRight(base, "/")]
	if !ok || now().Sub(e.at) > TTL {
		return Info{}, false, false
	}
	for _, i := range e.infos {
		if i.ID == model {
			return i, true, true
		}
	}
	return Info{}, false, true
}

// Forget drops every remembered catalogue (tests).
func Forget() {
	mu.Lock()
	defer mu.Unlock()
	cache = map[string]entry{}
}
