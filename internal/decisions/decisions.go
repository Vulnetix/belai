// Package decisions is the transport seam for Jev-style decision models: a
// state plus typed questions in, one probability per answer out, with no
// generated text. It carries three backends behind one interface:
//
//   - OpenRouter's Decisions API (the hosted TypeSafe Jev model), implemented
//     in internal/rolemanager/jev because it rides the OpenRouter SDK;
//   - a server speaking the /v1/systemone API (TypeSafe's hosted Jev, a
//     self-hosted laya-serve, decider.serve or jevk5-serve, and Strands
//     Decider-2B run by its own strands-decider server), in systemone.go;
//   - a local decision model (Decider-4B or Plumb-4B) served by llama-server,
//     read from the option-letter log-probabilities of one position, in
//     llama.go.
//
// A decision call is tool-less by construction: the request holds a state and
// questions, nothing else. Callers (the security guard, intent detection and
// routing in internal/rolemanager/jev) keep their own question wording and
// thresholds; this package only moves the question and the probabilities.
package decisions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Backend names a decision transport.
type Backend string

const (
	// BackendOpenRouter is the hosted Jev model on OpenRouter's Decisions API.
	BackendOpenRouter Backend = "openrouter"
	// BackendSystemOne is a self-hosted server speaking /v1/systemone.
	BackendSystemOne Backend = "systemone"
	// BackendLocal is a local decision model behind llama-server.
	BackendLocal Backend = "local"
)

// LocalProvider is the built-in provider name of the local decision server.
const LocalProvider = "decision-local"

// TypeSafeProvider is the built-in decision provider for TypeSafe's hosted
// native API. It is a classifier-only provider: never a chat provider, never
// in the provider registry, never routed through a firewall.
const TypeSafeProvider = "typesafe"

// TypeSafeBaseURL is TypeSafe's hosted API origin. It is fixed: the key is
// only ever sent here.
const TypeSafeBaseURL = "https://api.typesafe.ai"

// TypeSafeDefaultModel is the model TypeSafe serves when none is named.
const TypeSafeDefaultModel = "jev-latest"

// TypeSafeModels lists the TypeSafe model ids the /model picker offers.
var TypeSafeModels = []string{TypeSafeDefaultModel, "jev-1.13.0"}

// TypeSafeKeyEnv is the environment variable holding the TypeSafe API key.
const TypeSafeKeyEnv = "TYPESAFE_API_KEY"

// SystemOneKind is the provider-profile kind of a server speaking the
// /v1/systemone decision API.
const SystemOneKind = "systemone"

// LegacySystemOneKind is SystemOneKind's name before the rename; it is read
// as SystemOneKind and never written.
const LegacySystemOneKind = "jev"

// IsSystemOneKind reports whether a provider-profile kind names a
// /v1/systemone server, under its current or its legacy name.
func IsSystemOneKind(kind string) bool {
	return kind == SystemOneKind || kind == LegacySystemOneKind
}

// BackendOf reports which decision backend a provider/model pair names, if
// any. kind is the provider profile's kind for a custom provider ("" for a
// built-in). It is the one predicate that keeps decision models away from
// chat/completions.
func BackendOf(provider, kind, model string) (Backend, bool) {
	switch {
	case provider == "openrouter" && (strings.HasPrefix(model, "typesafe/jev") || IsDeciderID(model)):
		return BackendOpenRouter, true
	case provider == LocalProvider:
		return BackendLocal, true
	case provider == TypeSafeProvider, provider == DeciderProvider:
		return BackendSystemOne, true
	case IsHostedClef(provider, model):
		return BackendSystemOne, true
	case IsSystemOneKind(kind):
		return BackendSystemOne, true
	}
	return "", false
}

// Question types.
const (
	TypeNoul   = "noul"
	TypeChoice = "choice"
)

// Question is one typed question. A noul question is a proposition whose
// answer is P(true). A choice question has Options in a fixed order;
// Descriptions optionally explain an option.
type Question struct {
	Type         string
	Instructions string
	Options      []string
	Descriptions map[string]string
}

// Noul builds a noul question.
func Noul(instructions string) Question {
	return Question{Type: TypeNoul, Instructions: instructions}
}

// Request is one decision request: a state and the questions asked of it.
type Request struct {
	// State is a string or a JSON-marshallable map.
	State     any
	Questions map[string]Question
}

// Answer is one answer. Noul is P(true) for a noul question; Probabilities
// holds one probability per option for a choice question.
type Answer struct {
	Type          string
	Noul          float64
	Choice        string
	Probabilities map[string]float64
}

// Meta is what a call measured about itself, for the /model test report.
type Meta struct {
	Latency time.Duration
	// LetterMass is the share of the next-token probability the option
	// letters held (local backend only; 1 elsewhere). A low mass means the
	// prompt did not land the model at its answer slot.
	LetterMass float64
	// Path is the endpoint path that answered.
	Path string
}

// Result is a decision call's answers.
type Result struct {
	Answers map[string]Answer
	Meta    Meta
}

// Decider answers decision requests.
type Decider interface {
	Decide(ctx context.Context, r Request) (Result, error)
	// Identity names the backend and model for traces and fallback records,
	// e.g. "decision-local/decider-4b".
	Identity() string
	// Backend reports which transport this is.
	Backend() Backend
}

// Class names why a decision call failed.
type Class string

const (
	// ClassUnavailable is a failure the caller hands to its fallback: no
	// answer in time, a transport error, 429 or 5xx, an oversized state, or a
	// local answer that did not land on the option letters.
	ClassUnavailable Class = "unavailable"
	// ClassAuth is a refused credential (401/403).
	ClassAuth Class = "auth"
	// ClassNotFound is a wrong path or unknown model (404/405).
	ClassNotFound Class = "not-found"
	// ClassSchema is a request the server rejected (400/422) or an answer
	// that does not match the question.
	ClassSchema Class = "schema"
	// ClassProtocol is a TLS/HTTP mismatch or a response that is not JSON.
	ClassProtocol Class = "protocol"
)

// Error is a failed decision call.
type Error struct {
	Status int
	Class  Class
	Msg    string
	Err    error
}

func (e *Error) Error() string {
	s := "decisions: " + e.Msg
	if e.Status != 0 {
		s = fmt.Sprintf("decisions: HTTP %d: %s", e.Status, e.Msg)
	}
	if e.Err != nil {
		s += ": " + truncate(e.Err.Error(), 300)
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// IsUnavailable reports whether err is a decision failure the caller should
// hand to its fallback rather than surface: the backend did not answer
// usefully, as opposed to refusing the request.
func IsUnavailable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Class == ClassUnavailable
	}
	return false
}

// ClassOf returns the class of a decision error ("" for other errors).
func ClassOf(err error) Class {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ""
}

// StatusOf returns the HTTP status of a decision error (0 for none).
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// classForStatus maps an HTTP status to a failure class.
func classForStatus(code int) Class {
	switch {
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return ClassAuth
	case code == http.StatusNotFound || code == http.StatusMethodNotAllowed:
		return ClassNotFound
	case code == http.StatusBadRequest || code == http.StatusUnprocessableEntity:
		return ClassSchema
	case code == http.StatusTooManyRequests || code >= 500 || code == http.StatusRequestTimeout:
		return ClassUnavailable
	}
	return ClassProtocol
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// noRedirect refuses every redirect: a decision request carries tool output
// and a credential, and neither follows a server somewhere else.
func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// validate checks an answer set against the questions asked: every question
// answered, probabilities in [0,1], choices among the options. A broken
// answer is a schema error, never an approval.
func validate(q map[string]Question, answers map[string]Answer) error {
	for id, question := range q {
		a, ok := answers[id]
		if !ok {
			return &Error{Class: ClassSchema, Msg: "missing answer for " + id}
		}
		switch question.Type {
		case TypeNoul:
			if a.Noul < 0 || a.Noul > 1 || a.Noul != a.Noul {
				return &Error{Class: ClassSchema, Msg: fmt.Sprintf("noul out of range for %s", id)}
			}
		case TypeChoice:
			for k, p := range a.Probabilities {
				if p < 0 || p > 1 || p != p {
					return &Error{Class: ClassSchema, Msg: fmt.Sprintf("probability out of range for %s", id)}
				}
				if !contains(question.Options, k) {
					return &Error{Class: ClassSchema, Msg: fmt.Sprintf("answer %q for %s is not an offered option", k, id)}
				}
			}
			if a.Choice != "" && !contains(question.Options, a.Choice) {
				return &Error{Class: ClassSchema, Msg: fmt.Sprintf("choice %q for %s is not an offered option", a.Choice, id)}
			}
		}
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
