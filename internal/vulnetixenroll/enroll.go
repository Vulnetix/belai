// Package vulnetixenroll creates a Vulnetix account through the Authentik
// enrollment flow at auth.vulnetix.com, the same flow the web sign-up page
// runs (/if/flow/vulnetix-enrollment/), driven over its flow-executor API.
//
// The email and password go to that one host over TLS and nowhere else: the
// client refuses redirects off it, never logs or stores the form, and its
// results carry only the flow's own messages, sanitized for display.
package vulnetixenroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/sanitize"
)

const (
	// BaseURL is the Vulnetix identity provider.
	BaseURL = "https://auth.vulnetix.com"
	// FlowSlug is the enrollment flow.
	FlowSlug = "vulnetix-enrollment"

	requestTimeout = 20 * time.Second
	maxBody        = 256 << 10
	maxMessage     = 300
)

// Form is what the sign-up asks for. Company is optional.
type Form struct {
	Email          string
	Company        string
	Password       string
	PasswordRepeat string
}

// Clear overwrites the form's secrets so they do not linger in the view.
func (f *Form) Clear() {
	f.Password, f.PasswordRepeat = "", ""
}

// Outcome classifies what the flow did with a submission.
type Outcome int

const (
	// Invalid means the flow rejected fields; Result.Errors names them.
	Invalid Outcome = iota
	// VerifyEmail means the account waits on a link sent to the email.
	VerifyEmail
	// Done means the flow finished and the account exists.
	Done
	// Denied means the flow refused the sign-up.
	Denied
	// Browser means the flow reached a stage only the web page can run.
	Browser
)

// Result is a submission's outcome.
type Result struct {
	Outcome Outcome
	// Errors maps Form field names (email, company, password,
	// password_repeat, or "" for the form as a whole) to the flow's message.
	Errors map[string]string
	// Message is the flow's own text for the user, sanitized.
	Message string
	// URL is the web page to finish in, for Browser.
	URL string
}

// challenge is the part of an Authentik flow challenge the client reads.
type challenge struct {
	Component string `json:"component"`
	FlowInfo  struct {
		Title string `json:"title"`
	} `json:"flow_info"`
	Fields []struct {
		Key      string `json:"field_key"`
		Required bool   `json:"required"`
	} `json:"fields"`
	ResponseErrors map[string][]struct {
		String string `json:"string"`
		Code   string `json:"code"`
	} `json:"response_errors"`
	To           string `json:"to"`
	ErrorMessage string `json:"error_message"`
}

// Client drives one enrollment. Its cookie jar carries the flow session
// between Begin and Submit, so a Client is used for one sign-up only.
type Client struct {
	base string
	http *http.Client
}

// New returns a client for auth.vulnetix.com.
func New() *Client { return newClient(BaseURL, nil) }

func newClient(base string, transport http.RoundTripper) *Client {
	jar, _ := cookiejar.New(nil)
	origin, _ := url.Parse(base)
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{
		Timeout:   requestTimeout,
		Jar:       jar,
		Transport: transport,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Host != origin.Host || req.URL.Scheme != origin.Scheme {
				return errors.New("the sign-up flow redirected off " + origin.Host)
			}
			return nil
		},
	}}
}

func (c *Client) executorURL() string {
	return c.base + "/api/v3/flows/executor/" + FlowSlug + "/?query="
}

// WebURL is the sign-up page, for finishing in a browser.
func (c *Client) WebURL() string { return c.base + "/if/flow/" + FlowSlug + "/" }

func (c *Client) do(ctx context.Context, method string, body any) (challenge, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return challenge{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.executorURL(), rd)
	if err != nil {
		return challenge{}, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The error names the URL, never the body.
		return challenge{}, fmt.Errorf("could not reach %s: %w", BaseURL, unwrapURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return challenge{}, fmt.Errorf("the sign-up service answered HTTP %d", resp.StatusCode)
	}
	var ch challenge
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&ch); err != nil {
		return challenge{}, errors.New("the sign-up service sent an unexpected response")
	}
	return ch, nil
}

func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Begin opens the flow and checks it still asks for what Form holds.
func (c *Client) Begin(ctx context.Context) error {
	ch, err := c.do(ctx, http.MethodGet, nil)
	if err != nil {
		return err
	}
	if ch.Component != "ak-stage-prompt" {
		return fmt.Errorf("the sign-up flow starts with %q, not a form this client knows", clean(ch.Component))
	}
	have := map[string]bool{}
	for _, f := range ch.Fields {
		have[f.Key] = true
		if f.Required && !knownField[f.Key] {
			return fmt.Errorf("the sign-up form now requires %q, which this client does not collect", clean(f.Key))
		}
	}
	for _, k := range []string{"email", "password", "password_repeat"} {
		if !have[k] {
			return fmt.Errorf("the sign-up form no longer asks for %q", k)
		}
	}
	return nil
}

var knownField = map[string]bool{
	"email": true, "attributes.company": true, "password": true, "password_repeat": true,
}

// Submit posts the form to the prompt stage Begin opened.
func (c *Client) Submit(ctx context.Context, f Form) (Result, error) {
	if msg := validate(f); msg != nil {
		return Result{Outcome: Invalid, Errors: msg}, nil
	}
	ch, err := c.do(ctx, http.MethodPost, map[string]string{
		"component":          "ak-stage-prompt",
		"email":              strings.TrimSpace(f.Email),
		"attributes.company": strings.TrimSpace(f.Company),
		"password":           f.Password,
		"password_repeat":    f.PasswordRepeat,
	})
	if err != nil {
		return Result{}, err
	}
	return c.interpret(ch), nil
}

// validate catches what the flow would reject anyway, without a round trip.
func validate(f Form) map[string]string {
	out := map[string]string{}
	email := strings.TrimSpace(f.Email)
	if at := strings.LastIndex(email, "@"); at < 1 || at == len(email)-1 || strings.ContainsAny(email, " \t") {
		out["email"] = "Enter a valid email address."
	}
	if f.Password == "" {
		out["password"] = "Enter a password."
	}
	if f.Password != f.PasswordRepeat {
		out["password_repeat"] = "The passwords do not match."
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *Client) interpret(ch challenge) Result {
	switch ch.Component {
	case "ak-stage-prompt":
		if len(ch.ResponseErrors) > 0 {
			errs := map[string]string{}
			keys := make([]string, 0, len(ch.ResponseErrors))
			for k := range ch.ResponseErrors {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if msgs := ch.ResponseErrors[k]; len(msgs) > 0 {
					errs[formField(k)] = clean(msgs[0].String)
				}
			}
			return Result{Outcome: Invalid, Errors: errs}
		}
		// A further prompt stage asks for something this client does not.
		return c.browser(ch, "The sign-up needs a step this terminal cannot show.")
	case "ak-stage-email":
		return Result{Outcome: VerifyEmail, Message: "Check your inbox and open the verification link to activate the account."}
	case "xak-flow-redirect", "ak-stage-user-login":
		return Result{Outcome: Done, Message: "Account created."}
	case "ak-stage-access-denied":
		msg := clean(ch.ErrorMessage)
		if msg == "" {
			msg = "The sign-up was refused."
		}
		return Result{Outcome: Denied, Message: msg}
	default:
		return c.browser(ch, "The sign-up continues in the browser.")
	}
}

func (c *Client) browser(ch challenge, msg string) Result {
	if t := clean(ch.FlowInfo.Title); t != "" {
		msg += " (" + t + ")"
	}
	return Result{Outcome: Browser, Message: msg, URL: c.WebURL()}
}

// formField maps a flow field key to the Form's name for it.
func formField(k string) string {
	switch k {
	case "attributes.company":
		return "company"
	case "non_field_errors":
		return ""
	}
	return k
}

// clean flattens and bounds the flow's text before it is shown.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, ansi.Strip(sanitize.Sanitize(s)))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxMessage {
		s = string(r[:maxMessage]) + "…"
	}
	return s
}
