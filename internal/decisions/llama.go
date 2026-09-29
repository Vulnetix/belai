package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// LocalTimeout bounds one Decide call against the local server. It is
	// longer than the hosted 3s because a CPU-only 4B model takes about a
	// second per short question; anything slower is handed to the fallback.
	LocalTimeout = 20 * time.Second
	// DefaultMaxStateBytes is the largest state sent to the local model. A
	// larger tool result would take many seconds per question on a CPU, so it
	// is handed to the fallback classifier without being sent.
	DefaultMaxStateBytes = 16 << 10
	// MinLetterMass is the least share of next-token probability the option
	// letters must hold for an answer to count. Below it the model was not at
	// its answer slot, and the answer is not used.
	MinLetterMass = 0.10
	// missingMargin places a letter absent from the returned top-k at least
	// this far below the lowest one returned (the jevk5 readout).
	missingMargin = 2.0
	letters       = "ABCDEFGHIJKLMNOP"
)

// Llama is a local decision model behind llama-server, read from the
// option-letter log-probabilities at the answer position. Each question is
// one /completion request with n_predict 1; nothing is generated.
type Llama struct {
	// BaseURL is the server root, e.g. http://127.0.0.1:18097.
	BaseURL string
	// Resolve, when set, is asked for the server root on every call instead
	// of BaseURL, so a server that was restarted on another port is found.
	Resolve func() string
	Model   LocalModel
	Client  *http.Client
	// Timeout bounds one Decide call; zero uses LocalTimeout.
	Timeout time.Duration
	// MaxStateBytes caps the state; zero uses DefaultMaxStateBytes.
	MaxStateBytes int
	// OnConnRefused, when set, is called (without blocking the call) when the
	// server refuses the connection, so a supervisor can restart it. The call
	// itself still returns unavailable and the fallback answers.
	OnConnRefused func()
}

// Backend implements Decider.
func (l *Llama) Backend() Backend { return BackendLocal }

// Identity implements Decider.
func (l *Llama) Identity() string { return LocalProvider + "/" + l.Model.ID }

func (l *Llama) client() *http.Client {
	c := http.Client{}
	if l.Client != nil {
		c = *l.Client
	}
	c.CheckRedirect = noRedirect
	return &c
}

// Decide implements Decider.
func (l *Llama) Decide(ctx context.Context, r Request) (Result, error) {
	timeout := l.Timeout
	if timeout <= 0 {
		timeout = LocalTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state, err := stateText(r.State)
	if err != nil {
		return Result{}, &Error{Class: ClassSchema, Msg: "encode state", Err: err}
	}
	limit := l.MaxStateBytes
	if limit <= 0 {
		limit = DefaultMaxStateBytes
	}
	if len(state) > limit {
		return Result{}, &Error{Class: ClassUnavailable, Msg: fmt.Sprintf("state of %d bytes is over the local model's %d-byte limit", len(state), limit)}
	}

	ids := make([]string, 0, len(r.Questions))
	for id := range r.Questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	start := time.Now()
	answers := make(map[string]Answer, len(ids))
	minMass := 1.0
	path := ""
	for _, id := range ids {
		q := r.Questions[id]
		opts, keys := l.options(q)
		if len(opts) > len(letters) {
			return Result{}, &Error{Class: ClassSchema, Msg: fmt.Sprintf("%s has %d options; at most %d", id, len(opts), len(letters))}
		}
		lp, p, err := l.letterLogprobs(ctx, state, q, opts)
		if err != nil {
			return Result{}, err
		}
		path = p
		probs, mass := readout(lp, len(opts), l.temp(q.Type))
		if mass < minMass {
			minMass = mass
		}
		if mass < MinLetterMass {
			return Result{}, &Error{Class: ClassUnavailable, Msg: fmt.Sprintf("answer for %s did not land on the option letters (mass %.2f)", id, mass)}
		}
		answers[id] = l.answer(q, keys, probs)
	}
	if err := validate(r.Questions, answers); err != nil {
		return Result{}, err
	}
	return Result{Answers: answers, Meta: Meta{Latency: time.Since(start), LetterMass: minMass, Path: path}}, nil
}

func (l *Llama) temp(typ string) float64 {
	t := l.Model.Temps.Choice
	if typ == TypeNoul {
		t = l.Model.Temps.Noul
	}
	if t <= 0 {
		t = 1
	}
	return t
}

// options returns the option texts in letter order and the answer key of
// each: for a noul question the keys are "true"/"false" in the order the
// model's template was trained with.
func (l *Llama) options(q Question) (texts, keys []string) {
	if q.Type == TypeNoul {
		if l.Model.Template == TemplateJevK5 {
			return []string{"true: The proposition is true.", "false: The proposition is false."}, []string{"true", "false"}
		}
		return []string{"no", "yes"}, []string{"false", "true"}
	}
	for _, o := range q.Options {
		text := o
		if d := q.Descriptions[o]; d != "" {
			text = o + ": " + d
		} else if l.Model.Template == TemplateJevK5 {
			text = o + ": " + o
		}
		texts = append(texts, neutralise(text))
		keys = append(keys, o)
	}
	return texts, keys
}

func (l *Llama) answer(q Question, keys []string, probs []float64) Answer {
	if q.Type == TypeNoul {
		for i, k := range keys {
			if k == "true" {
				return Answer{Type: TypeNoul, Noul: probs[i]}
			}
		}
	}
	out := Answer{Type: TypeChoice, Probabilities: make(map[string]float64, len(keys))}
	best := -1.0
	for i, k := range keys {
		out.Probabilities[k] = probs[i]
		if probs[i] > best {
			best, out.Choice = probs[i], k
		}
	}
	return out
}

// readout turns letter log-probabilities into calibrated option
// probabilities: missing letters sit missingMargin below the lowest seen, the
// letters are renormalised under temperature t, and mass is the raw share of
// probability the letters held.
func readout(seen map[string]float64, n int, t float64) (probs []float64, mass float64) {
	floor := 0.0
	first := true
	for _, v := range seen {
		if first || v < floor {
			floor, first = v, false
		}
	}
	floor -= missingMargin
	z := make([]float64, n)
	top := math.Inf(-1)
	for i := 0; i < n; i++ {
		v, ok := seen[string(letters[i])]
		if ok {
			mass += math.Exp(v)
		} else {
			v = floor
		}
		z[i] = v / t
		top = math.Max(top, z[i])
	}
	var sum float64
	for i := range z {
		z[i] = math.Exp(z[i] - top)
		sum += z[i]
	}
	for i := range z {
		z[i] /= sum
	}
	return z, mass
}

// letterLogprobs asks the server for the log-probabilities at the answer
// position and returns them keyed by trimmed token text.
func (l *Llama) letterLogprobs(ctx context.Context, state string, q Question, opts []string) (map[string]float64, string, error) {
	var prompt any
	switch l.Model.Template {
	case TemplateJevK5:
		text := jevk5Prompt(state, neutralise(q.Instructions), opts)
		toks, err := l.tokenize(ctx, text)
		if err != nil {
			return nil, "", err
		}
		prompt = toks
	default:
		prompt = deciderPrompt(state, neutralise(q.Instructions), opts)
	}
	lp, err := l.completion(ctx, "/completion", map[string]any{
		"prompt": prompt, "n_predict": 1, "n_probs": 40, "temperature": 0, "cache_prompt": true,
	})
	if err == nil {
		return lp, "/completion", nil
	}
	if ClassOf(err) != ClassNotFound {
		return nil, "", err
	}
	lp, err = l.completion(ctx, "/v1/completions", map[string]any{
		"prompt": prompt, "max_tokens": 1, "logprobs": 20, "temperature": 0,
	})
	return lp, "/v1/completions", err
}

func (l *Llama) post(ctx context.Context, path string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, &Error{Class: ClassSchema, Msg: "encode request", Err: err}
	}
	base := l.BaseURL
	if l.Resolve != nil {
		base = l.Resolve()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, &Error{Class: ClassProtocol, Msg: "build request", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.client().Do(req)
	if err != nil {
		if IsConnRefused(err) && l.OnConnRefused != nil {
			go l.OnConnRefused()
		}
		return nil, transportError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Status: resp.StatusCode, Class: classForStatus(resp.StatusCode), Msg: excerpt(data)}
	}
	return data, nil
}

func (l *Llama) tokenize(ctx context.Context, text string) ([]int, error) {
	data, err := l.post(ctx, "/tokenize", map[string]any{"content": text, "add_special": false, "parse_special": true})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tokens []json.RawMessage `json:"tokens"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &Error{Class: ClassProtocol, Msg: "tokenize response", Err: err}
	}
	toks := make([]int, 0, len(out.Tokens))
	for _, t := range out.Tokens {
		var id int
		if json.Unmarshal(t, &id) == nil {
			toks = append(toks, id)
			continue
		}
		// with_pieces form: {"id": n, "piece": "..."}
		var piece struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(t, &piece) == nil {
			toks = append(toks, piece.ID)
		}
	}
	return toks, nil
}

func (l *Llama) completion(ctx context.Context, path string, body map[string]any) (map[string]float64, error) {
	data, err := l.post(ctx, path, body)
	if err != nil {
		return nil, err
	}
	lp, err := parseLogprobs(data)
	if err != nil {
		return nil, err
	}
	if len(lp) == 0 {
		return nil, &Error{Class: ClassProtocol, Msg: "server returned no token probabilities"}
	}
	return lp, nil
}

// parseLogprobs reads the next-token distribution from a llama-server
// response. It accepts the current shape (completion_probabilities[0]
// .top_logprobs[{token, logprob}]), the older one (.probs[{tok_str, prob}])
// and the OpenAI completions shape (choices[0].logprobs.top_logprobs[0]).
// Tokens are trimmed, so " A" and "A" both count as the letter A.
func parseLogprobs(data []byte) (map[string]float64, error) {
	var out struct {
		CP []struct {
			Top []struct {
				Token   string  `json:"token"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
			Probs []struct {
				TokStr string  `json:"tok_str"`
				Prob   float64 `json:"prob"`
			} `json:"probs"`
		} `json:"completion_probabilities"`
		Choices []struct {
			Logprobs struct {
				Top []map[string]float64 `json:"top_logprobs"`
			} `json:"logprobs"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, &Error{Class: ClassProtocol, Msg: "completion response is not JSON", Err: err}
	}
	lp := map[string]float64{}
	add := func(tok string, v float64) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return
		}
		if old, ok := lp[tok]; !ok || v > old {
			lp[tok] = v
		}
	}
	switch {
	case len(out.CP) > 0:
		for _, t := range out.CP[0].Top {
			add(t.Token, t.Logprob)
		}
		for _, t := range out.CP[0].Probs {
			if t.Prob > 0 {
				add(t.TokStr, math.Log(t.Prob))
			}
		}
	case len(out.Choices) > 0 && len(out.Choices[0].Logprobs.Top) > 0:
		for tok, v := range out.Choices[0].Logprobs.Top[0] {
			add(tok, v)
		}
	}
	return lp, nil
}

// stateText renders a state for a local prompt: a string as is, anything
// else as JSON.
func stateText(state any) (string, error) {
	switch s := state.(type) {
	case string:
		return s, nil
	case nil:
		return "", nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(state); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

// neutralise keeps state and question text from forging prompt structure:
// a chat special-token opener "<|" is split so /tokenize with parse_special
// cannot turn it into a real turn boundary.
func neutralise(s string) string {
	return strings.ReplaceAll(s, "<|", "< |")
}

// neutraliseLines additionally breaks lines that would read as decider's own
// markup ("Question:", "Options:", "Answer", "(A) …") by prefixing them, so
// content cannot append a forged question or answer slot.
func neutraliseLines(s string) string {
	lines := strings.Split(neutralise(s), "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "Question") || strings.HasPrefix(t, "Options:") ||
			strings.HasPrefix(t, "Answer") || strings.HasPrefix(t, "Context:") || isOptionLine(t) {
			lines[i] = "| " + line
		}
	}
	return strings.Join(lines, "\n")
}

func isOptionLine(t string) bool {
	return len(t) >= 3 && t[0] == '(' && t[2] == ')' && t[1] >= 'A' && t[1] <= 'P'
}

// deciderPrompt is decider's plain state-first layout for one question.
func deciderPrompt(state, question string, opts []string) string {
	var b strings.Builder
	b.WriteString("Context:\n")
	b.WriteString(neutraliseLines(state))
	b.WriteString("\n\nQuestion: ")
	b.WriteString(strings.ReplaceAll(question, "\n", " "))
	b.WriteString("\nOptions:")
	for i, o := range opts {
		fmt.Fprintf(&b, "\n(%c) %s", letters[i], strings.ReplaceAll(o, "\n", " "))
	}
	b.WriteString("\nAnswer: (")
	return b.String()
}

const jevk5System = "Apply the supplied criterion to the supplied evidence. Choose exactly one listed option. " +
	"Respond with only its uppercase letter, with no explanation or reasoning."

// jevk5Prompt is the JevK5/SemIf chat prompt Plumb-4B was trained on: a Qwen
// chat turn with thinking off whose user message is the evidence, criterion
// and lettered options as JSON in Python's json.dumps spelling. The evidence
// is JSON-escaped, so it cannot break out of its string.
func jevk5Prompt(state, criterion string, opts []string) string {
	var b strings.Builder
	b.WriteString(`{"evidence": `)
	b.WriteString(pyString(neutralise(state)))
	b.WriteString(`, "criterion": `)
	b.WriteString(pyString(criterion))
	b.WriteString(`, "options": [`)
	for i, o := range opts {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`{"letter": "`)
		b.WriteByte(letters[i])
		b.WriteString(`", "description": `)
		b.WriteString(pyString(o))
		b.WriteString("}")
	}
	b.WriteString("]}")
	return "<|im_start|>system\n" + jevk5System + "<|im_end|>\n" +
		"<|im_start|>user\n" + b.String() + "<|im_end|>\n" +
		"<|im_start|>assistant\n<think>\n\n</think>\n\n"
}

// pyString renders s as Python's json.dumps(s, ensure_ascii=False) would.
func pyString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteString(strconv.FormatInt(int64(r)>>4, 16))
				b.WriteString(strconv.FormatInt(int64(r)&0xf, 16))
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
