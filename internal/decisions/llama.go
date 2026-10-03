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
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/sanitize"
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
	// letters are the option letters in order. Tev1 reads all 24; the other
	// templates read the first maxLetters.
	letters    = "ABCDEFGHIJKLMNOPQRSTUVWX"
	maxLetters = 16
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

// systemOne answers through llama-server's own /v1/systemone endpoint, for a
// model the server reads itself (Clef). No prompt layout is composed here, and
// a refused connection still reaches the supervisor.
func (l *Llama) systemOne(ctx context.Context, r Request) (Result, error) {
	base := l.BaseURL
	if l.Resolve != nil {
		if u := l.Resolve(); u != "" {
			base = u
		}
	}
	s := &SystemOne{
		Name:           LocalProvider,
		Model:          l.Model.ID,
		BaseURL:        base,
		Path:           DefaultSystemOnePath,
		Client:         l.Client,
		Timeout:        LocalTimeout,
		CriteriaObject: true,
		MaxOptions:     l.Model.MaxOptions,
		OnConnRefused:  l.OnConnRefused,
	}
	if dl, ok := ctx.Deadline(); ok {
		s.Timeout = time.Until(dl)
	}
	return s.Decide(ctx, r)
}

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
	if l.Model.Template == TemplateSystemOne {
		return l.systemOne(ctx, r)
	}
	max := maxLetters
	if l.Model.Template == TemplateTev1 {
		max = Tev1MaxOptions
	}
	return decideByLetters(ctx, r, letterReader{
		max:     max,
		options: l.options,
		temp:    l.temp,
		ask:     l.letterLogprobs,
	})
}

// letterReader is how one transport reads a question from the option
// letters: the options it lays out, its calibration, and the call that
// returns the next-token log-probabilities at the answer slot.
type letterReader struct {
	// max is the most options a question may carry.
	max int
	// parallel is how many questions are asked at once; zero or one asks
	// them in turn.
	parallel int
	options  func(Question) (texts, keys []string)
	temp     func(typ string) float64
	ask      func(ctx context.Context, state string, q Question, texts, keys []string) (map[string]float64, string, error)
}

// decideByLetters asks each question once and reads the answer from the
// letters' log-probabilities. An answer whose letters hold less than
// MinLetterMass is unavailable, so the fallback answers instead.
func decideByLetters(ctx context.Context, r Request, lr letterReader) (Result, error) {
	state, err := stateText(r.State)
	if err != nil {
		return Result{}, &Error{Class: ClassSchema, Msg: "encode state", Err: err}
	}
	ids := make([]string, 0, len(r.Questions))
	for id := range r.Questions {
		ids = append(ids, id)
		if q := r.Questions[id]; q.Type != TypeNoul && len(q.Options) > lr.max {
			return Result{}, &Error{Class: ClassSchema, Msg: fmt.Sprintf("%s has %d options; at most %d", id, len(q.Options), lr.max)}
		}
	}
	sort.Strings(ids)

	type read struct {
		answer Answer
		mass   float64
		path   string
		err    error
	}
	start := time.Now()
	reads := make([]read, len(ids))
	one := func(i int) {
		q := r.Questions[ids[i]]
		texts, keys := lr.options(q)
		lp, path, err := lr.ask(ctx, state, q, texts, keys)
		if err != nil {
			reads[i] = read{err: err}
			return
		}
		probs, mass := readout(lp, len(texts), lr.temp(q.Type))
		if mass < MinLetterMass {
			reads[i] = read{mass: mass, err: &Error{Class: ClassUnavailable, Msg: fmt.Sprintf("answer for %s did not land on the option letters (mass %.2f)", ids[i], mass)}}
			return
		}
		reads[i] = read{answer: answerOf(q, keys, probs), mass: mass, path: path}
	}
	if lr.parallel > 1 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, lr.parallel)
		for i := range ids {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer func() { <-sem; wg.Done() }()
				one(i)
			}(i)
		}
		wg.Wait()
	} else {
		for i := range ids {
			if one(i); reads[i].err != nil {
				return Result{}, reads[i].err
			}
		}
	}

	answers := make(map[string]Answer, len(ids))
	minMass := 1.0
	path := ""
	for i, id := range ids {
		if reads[i].err != nil {
			return Result{}, reads[i].err
		}
		answers[id] = reads[i].answer
		minMass = math.Min(minMass, reads[i].mass)
		path = reads[i].path
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
	if l.Model.Template == TemplateTev1 {
		return tev1Options(q)
	}
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

func answerOf(q Question, keys []string, probs []float64) Answer {
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
func (l *Llama) letterLogprobs(ctx context.Context, state string, q Question, opts, keys []string) (map[string]float64, string, error) {
	var prompt any
	switch l.Model.Template {
	case TemplateJevK5, TemplateTev1:
		text := jevk5Prompt(state, neutralise(q.Instructions), opts)
		if l.Model.Template == TemplateTev1 {
			text = tev1Prompt(state, neutralise(q.Instructions), opts, keys)
		}
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
// the OpenAI completions shape (choices[0].logprobs.top_logprobs[0]) and the
// OpenAI chat shape (choices[0].logprobs.content[0].top_logprobs).
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
				Top     []map[string]float64 `json:"top_logprobs"`
				Content []struct {
					Top []struct {
						Token   string  `json:"token"`
						Logprob float64 `json:"logprob"`
					} `json:"top_logprobs"`
				} `json:"content"`
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
	case len(out.Choices) > 0 && len(out.Choices[0].Logprobs.Content) > 0:
		for _, t := range out.Choices[0].Logprobs.Content[0].Top {
			add(t.Token, t.Logprob)
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

// neutralise keeps state and question text from forging prompt structure. It
// is sanitize.ForDecision: chat special-token openers are split in any width
// and case, lines that imitate the prompt's own layout are prefixed, and
// control, bidi and zero-width runes are removed.
func neutralise(s string) string { return sanitize.ForDecision(s, 0).String() }

// neutraliseLines is neutralise; the layout-line rule is part of it now.
func neutraliseLines(s string) string { return neutralise(s) }

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
