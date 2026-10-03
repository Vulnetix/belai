package decisions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChatLettersTimeout bounds one hosted decision call, every question in it.
const ChatLettersTimeout = 10 * time.Second

// ChatLettersMaxStateBytes is the largest state sent to a hosted letter
// model. A larger tool result is handed to the fallback without being sent.
const ChatLettersMaxStateBytes = 32 << 10

// chatLettersParallel is how many questions of one request are in flight.
const chatLettersParallel = 4

// ChatLetters is a decision model served on an OpenAI-compatible chat
// completions endpoint and read from the answer letter's log-probabilities:
// Tev1 on Together. Each question is one tool-less request for one token at
// temperature 0 with thinking off, so the model is never asked to chat. The
// key rides only in the Authorization header to BaseURL; redirects are
// refused. A response without log-probabilities is unavailable rather than
// read from its text, so an unscored letter never decides anything.
type ChatLetters struct {
	// Name is the provider name, for Identity.
	Name string
	// BaseURL is the API root, e.g. https://api.together.xyz/v1.
	BaseURL string
	Model   string
	Key     func() (string, error)
	Client  *http.Client
	// Timeout bounds one call; zero uses ChatLettersTimeout.
	Timeout time.Duration
	// MaxStateBytes caps the state; zero uses ChatLettersMaxStateBytes.
	MaxStateBytes int
}

// Backend implements Decider.
func (c *ChatLetters) Backend() Backend { return BackendChatLetters }

// Identity implements Decider.
func (c *ChatLetters) Identity() string { return c.Name + "/" + c.Model }

func (c *ChatLetters) client() *http.Client {
	hc := http.Client{}
	if c.Client != nil {
		hc = *c.Client
	}
	hc.CheckRedirect = noRedirect
	return &hc
}

// Decide implements Decider.
func (c *ChatLetters) Decide(ctx context.Context, r Request) (Result, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = ChatLettersTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	state, err := stateText(r.State)
	if err != nil {
		return Result{}, &Error{Class: ClassSchema, Msg: "encode state", Err: err}
	}
	limit := c.MaxStateBytes
	if limit <= 0 {
		limit = ChatLettersMaxStateBytes
	}
	if len(state) > limit {
		return Result{}, &Error{Class: ClassUnavailable, Msg: fmt.Sprintf("state of %d bytes is over the hosted model's %d-byte limit", len(state), limit)}
	}
	key := ""
	if c.Key != nil {
		if key, err = c.Key(); err != nil {
			return Result{}, &Error{Class: ClassAuth, Msg: "resolve key", Err: err}
		}
	}
	return decideByLetters(ctx, r, letterReader{
		max:      Tev1MaxOptions,
		parallel: chatLettersParallel,
		options:  tev1Options,
		temp:     func(string) float64 { return 1 },
		ask: func(ctx context.Context, state string, q Question, texts, keys []string) (map[string]float64, string, error) {
			lp, err := c.ask(ctx, key, tev1Payload(state, neutralise(q.Instructions), texts, keys))
			return lp, "/chat/completions", err
		},
	})
}

func (c *ChatLetters) ask(ctx context.Context, key, user string) (map[string]float64, error) {
	raw, err := json.Marshal(map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": tev1System},
			{"role": "user", "content": user},
		},
		"temperature": 0, "max_tokens": 1,
		"logprobs": true, "top_logprobs": 20,
		"chat_template_kwargs": map[string]bool{"enable_thinking": false},
	})
	if err != nil {
		return nil, &Error{Class: ClassSchema, Msg: "encode request", Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, &Error{Class: ClassProtocol, Msg: "build request", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return nil, &Error{Status: resp.StatusCode, Class: ClassProtocol, Msg: "server redirected; redirects are not followed"}
		}
		return nil, &Error{Status: resp.StatusCode, Class: classForStatus(resp.StatusCode), Msg: excerpt(data)}
	}
	lp, err := parseLogprobs(data)
	if err != nil {
		return nil, err
	}
	if len(lp) == 0 {
		return nil, &Error{Class: ClassUnavailable, Msg: "the provider returned no log-probabilities for the answer"}
	}
	return lp, nil
}
