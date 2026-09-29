package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// SystemOne is a self-hosted Jev-compatible server speaking TypeSafe's native
// POST /v1/systemone API. The key, when set, rides only in the Authorization
// header of requests to BaseURL; redirects are refused.
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
}

// Backend implements Decider.
func (s *SystemOne) Backend() Backend { return BackendSystemOne }

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

	questions := make(map[string]wireQuestion, len(r.Questions))
	for id, q := range r.Questions {
		wq := wireQuestion{Type: q.Type, Instructions: q.Instructions}
		if q.Type == TypeChoice {
			if len(q.Descriptions) > 0 {
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
	if s.SendModel && s.Model != "" {
		body["model"] = s.Model
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Result{}, &Error{Class: ClassSchema, Msg: "encode request", Err: err}
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.BaseURL, "/")+s.path(), bytes.NewReader(raw))
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
	resp, err := s.client().Do(req)
	if err != nil {
		return Result{}, transportError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return Result{}, &Error{Status: resp.StatusCode, Class: ClassProtocol, Msg: "server redirected; redirects are not followed"}
		}
		return Result{}, &Error{Status: resp.StatusCode, Class: classForStatus(resp.StatusCode), Msg: excerpt(data)}
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
