package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/sanitize"
)

// DefaultSystemOnePath is TypeSafe's native decision endpoint.
const DefaultSystemOnePath = "/v1/systemone"

// SystemOneTimeout bounds one call to a self-hosted Jev server.
const SystemOneTimeout = 5 * time.Second

// SystemOne is a server speaking the POST /v1/systemone decision API:
// TypeSafe's hosted Jev, a self-hosted Jev server, or Strands Decider-2B. The
// key, when set, rides only in the Authorization header of requests to
// BaseURL; redirects are refused.
type SystemOne struct {
	// Name is the provider profile name, for Identity.
	Name string
	// BaseURL is the server origin (scheme://host:port[/prefix]).
	BaseURL string
	// Path is the decision endpoint; "" uses DefaultSystemOnePath.
	Path string
	// Model names the served model in Identity. It is sent as "model" only
	// when SendModel is set: some servers reject fields they do not know.
	Model     string
	SendModel bool
	Key       func() (string, error)
	Client    *http.Client
	// Timeout bounds one call; zero uses SystemOneTimeout.
	Timeout time.Duration
	// CriteriaObject sends a choice's criteria as an object of option to
	// description (null when it has none) even without descriptions. Strands
	// Decider's server takes only that form; TypeSafe's also takes a list.
	CriteriaObject bool
	// MaxOptions, when set, is the most options a question may carry; a
	// request with more is never sent and fails as unavailable, so the
	// caller's fallback answers instead of the server refusing it.
	MaxOptions int
	// Resolve, when set, supplies the base URL for each call (a local server
	// whose port the supervisor records); BaseURL is then a fallback.
	Resolve func() string
	// OnConnRefused, when set, is called when nothing listens at the URL, so
	// a local supervisor can start its server again.
	OnConnRefused func()
	// WireModel, when set, is the "model" value sent with SendModel in place
	// of Model, which stays the catalogue id Identity names (Workers AI takes
	// "clef" for @cf/cloudflare/clef).
	WireModel string
	// MaxQuestions, when set, is the most questions one request may carry; a
	// larger request is never sent and fails as unavailable.
	MaxQuestions int
	// MaxBodyBytes, when set, is the largest request body the server takes
	// (Ollama's is 64 KiB); a larger one is never sent and fails as
	// unavailable, so the fallback answers instead of the server refusing it.
	MaxBodyBytes int
	// Envelope reads Cloudflare's API envelope ({"result": …, "success": …,
	// "errors": […]}) around the answers. A bare answer body is read too.
	Envelope bool
	// ExtraHeaders, when set, adds harness-composed headers (AI Gateway's
	// cf-aig-authorization) to requests to BaseURL only; redirects are
	// refused, so they never reach another host.
	ExtraHeaders func() (map[string]string, error)
}

func (s *SystemOne) baseURL() string {
	if s.Resolve != nil {
		if u := s.Resolve(); u != "" {
			return u
		}
	}
	return s.BaseURL
}

// Backend implements Decider.
func (s *SystemOne) Backend() Backend { return BackendSystemOne }

// Hosted reports whether the server is a hosted API (TypeSafe's, or Clef on
// Cloudflare, which answers in its envelope) rather than one the user runs.
func (s *SystemOne) Hosted() bool {
	if s.Envelope {
		return true
	}
	u := strings.TrimRight(s.BaseURL, "/")
	return u == TypeSafeBaseURL || strings.HasPrefix(u, TypeSafeBaseURL+"/")
}

// Identity implements Decider.
func (s *SystemOne) Identity() string {
	id := s.Name
	if id == "" {
		id = "jev"
	}
	if s.Model != "" {
		id += "/" + s.Model
	}
	return id
}

func (s *SystemOne) path() string {
	if s.Path == "" {
		return DefaultSystemOnePath
	}
	return "/" + strings.TrimLeft(s.Path, "/")
}

func (s *SystemOne) client() *http.Client {
	c := http.Client{}
	if s.Client != nil {
		c = *s.Client
	}
	c.CheckRedirect = noRedirect
	return &c
}

type wireQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions,omitempty"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Decide implements Decider.
func (s *SystemOne) Decide(ctx context.Context, r Request) (Result, error) {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = SystemOneTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if s.MaxQuestions > 0 && len(r.Questions) > s.MaxQuestions {
		return Result{}, &Error{Class: ClassUnavailable, Msg: "a request has more questions than the model reads at once"}
	}
	questions := make(map[string]wireQuestion, len(r.Questions))
	for id, q := range r.Questions {
		if s.MaxOptions > 0 && len(q.Options) > s.MaxOptions {
			return Result{}, &Error{Class: ClassUnavailable, Msg: "a question has more options than the model reads"}
		}
		wq := wireQuestion{Type: q.Type, Instructions: q.Instructions}
		if q.Type == TypeChoice {
			if s.CriteriaObject {
				crit := make(map[string]*string, len(q.Options))
				for _, o := range q.Options {
					var d *string
					if v, ok := q.Descriptions[o]; ok && v != "" {
						d = &v
					}
					crit[o] = d
				}
				wq.Criteria = crit
			} else if len(q.Descriptions) > 0 {
				crit := make(map[string]string, len(q.Options))
				for _, o := range q.Options {
					crit[o] = q.Descriptions[o]
				}
				wq.Criteria = crit
			} else {
				wq.Criteria = q.Options
			}
		}
		questions[id] = wq
	}
	body := map[string]any{"state": r.State, "questions": questions}
	if s.SendModel {
		if m := s.wireModel(); m != "" {
			body["model"] = m
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Result{}, &Error{Class: ClassSchema, Msg: "encode request", Err: err}
	}
	if s.MaxBodyBytes > 0 && len(raw) > s.MaxBodyBytes {
		return Result{}, &Error{Class: ClassUnavailable, Msg: fmt.Sprintf("a request of %d bytes is over the server's %d-byte limit", len(raw), s.MaxBodyBytes)}
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.baseURL(), "/")+s.path(), bytes.NewReader(raw))
	if err != nil {
		return Result{}, &Error{Class: ClassProtocol, Msg: "build request", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Key != nil {
		key, err := s.Key()
		if err != nil {
			return Result{}, &Error{Class: ClassAuth, Msg: "resolve key", Err: err}
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	if s.ExtraHeaders != nil {
		h, err := s.ExtraHeaders()
		if err != nil {
			return Result{}, &Error{Class: ClassAuth, Msg: "resolve gateway credential", Err: err}
		}
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	resp, err := s.client().Do(req)
	if err != nil {
		if s.OnConnRefused != nil && IsConnRefused(err) {
			s.OnConnRefused()
		}
		return Result{}, transportError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return Result{}, &Error{Status: resp.StatusCode, Class: ClassProtocol, Msg: "server redirected; redirects are not followed"}
		}
		msg := excerpt(data)
		if s.Envelope {
			if m := envelopeError(data); m != "" {
				msg = m
			}
		}
		return Result{}, &Error{Status: resp.StatusCode, Class: classForStatus(resp.StatusCode), Msg: msg}
	}
	if s.Envelope {
		inner, err := unwrapEnvelope(data)
		if err != nil {
			return Result{}, err
		}
		data = inner
	}
	var out struct {
		Answers map[string]wireAnswer `json:"answers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return Result{}, &Error{Class: ClassProtocol, Msg: "response is not a systemone answer", Err: err}
	}
	answers := make(map[string]Answer, len(out.Answers))
	for id, a := range out.Answers {
		ans := Answer{Type: a.Type, Choice: a.Choice, Probabilities: a.Probabilities}
		if a.Noul != nil {
			ans.Noul = *a.Noul
		} else if a.Type == TypeNoul {
			ans.Noul = -1 // missing: fails validation
		}
		answers[id] = ans
	}
	if err := validate(r.Questions, answers); err != nil {
		return Result{}, err
	}
	return Result{Answers: answers, Meta: Meta{Latency: time.Since(start), LetterMass: 1, Path: s.path()}}, nil
}

func (s *SystemOne) wireModel() string {
	if s.WireModel != "" {
		return s.WireModel
	}
	return s.Model
}

// cfEnvelope is Cloudflare's API envelope.
type cfEnvelope struct {
	Result  json.RawMessage `json:"result"`
	Success *bool           `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// unwrapEnvelope returns the answers body inside Cloudflare's envelope. A
// body that already holds answers (a gateway that unwrapped it) is returned
// as it is. success:false is a refusal, worded from the first error only
// after sanitising.
func unwrapEnvelope(data []byte) ([]byte, error) {
	var probe struct {
		Answers json.RawMessage `json:"answers"`
	}
	if json.Unmarshal(data, &probe) == nil && len(probe.Answers) > 0 {
		return data, nil
	}
	var env cfEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, &Error{Class: ClassProtocol, Msg: "response is not a Cloudflare answer", Err: err}
	}
	if env.Success != nil && !*env.Success {
		msg := envelopeError(data)
		if msg == "" {
			msg = "Cloudflare reported a failure"
		}
		return nil, &Error{Class: ClassSchema, Msg: msg}
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return nil, &Error{Class: ClassProtocol, Msg: "Cloudflare answer has no result"}
	}
	return env.Result, nil
}

// envelopeError is the first error message of a Cloudflare envelope,
// sanitised and capped, or "".
func envelopeError(data []byte) string {
	var env cfEnvelope
	if json.Unmarshal(data, &env) != nil || len(env.Errors) == 0 {
		return ""
	}
	m := sanitize.Line(env.Errors[0].Message, 0)
	if m == "" {
		return ""
	}
	return truncate(m, 200)
}

// transportError classes a failure before any response arrived.
func transportError(err error) error {
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Class: ClassUnavailable, Msg: "no answer in time", Err: err}
	case strings.Contains(lower, "http: server gave http response to https client"),
		strings.Contains(lower, "first record does not look like a tls handshake"),
		strings.Contains(lower, "tls:"),
		strings.Contains(lower, "x509:"):
		return &Error{Class: ClassProtocol, Msg: "TLS/HTTP mismatch", Err: err}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &Error{Class: ClassUnavailable, Msg: "no answer in time", Err: err}
	}
	return &Error{Class: ClassUnavailable, Msg: "could not reach the server", Err: err}
}

// IsConnRefused reports whether a decision error is a refused connection
// (nothing listening), which a local server supervisor may recover from.
func IsConnRefused(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "connection refused")
}

func excerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	var env struct {
		Error any    `json:"error"`
		Msg   string `json:"message"`
		Det   any    `json:"detail"`
	}
	if json.Unmarshal(b, &env) == nil {
		switch {
		case env.Msg != "":
			s = env.Msg
		case env.Error != nil:
			if m, ok := env.Error.(map[string]any); ok {
				if v, ok := m["message"].(string); ok {
					s = v
				}
			} else if v, ok := env.Error.(string); ok {
				s = v
			}
		case env.Det != nil:
			if v, ok := env.Det.(string); ok {
				s = v
			}
		}
	}
	s = sanitize.Line(s, 0)
	if s == "" {
		return "empty response body"
	}
	return truncate(s, 200)
}
