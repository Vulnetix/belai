package vulnetixenroll

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// flowServer serves the recorded GET, then answers a POST with post. It
// checks the flow session cookie round-trips and records the posted form.
func flowServer(t *testing.T, get, post string) (*httptest.Server, *map[string]string) {
	t.Helper()
	posted := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/flows/executor/"+FlowSlug+"/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			http.SetCookie(w, &http.Cookie{Name: "authentik_session", Value: "s1", Path: "/"})
			io.WriteString(w, get)
		case http.MethodPost:
			if c, err := r.Cookie("authentik_session"); err != nil || c.Value != "s1" {
				t.Error("flow session cookie not sent")
			}
			json.NewDecoder(r.Body).Decode(&posted)
			io.WriteString(w, post)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &posted
}

var good = Form{Email: "you@example.com", Company: " Acme ", Password: "pw-1", PasswordRepeat: "pw-1"}

func submit(t *testing.T, post string) (Result, map[string]string) {
	t.Helper()
	srv, posted := flowServer(t, fixture(t, "prompt.json"), post)
	c := newClient(srv.URL, nil)
	if err := c.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := c.Submit(context.Background(), good)
	if err != nil {
		t.Fatal(err)
	}
	return res, *posted
}

func TestSubmitPostsThePromptStage(t *testing.T) {
	_, posted := submit(t, `{"component":"ak-stage-email"}`)
	want := map[string]string{
		"component": "ak-stage-prompt", "email": "you@example.com", "attributes.company": "Acme",
		"password": "pw-1", "password_repeat": "pw-1",
	}
	for k, v := range want {
		if posted[k] != v {
			t.Errorf("%s = %q, want %q", k, posted[k], v)
		}
	}
}

func TestSubmitMapsFieldErrors(t *testing.T) {
	res, _ := submit(t, fixture(t, "prompt_invalid_email.json"))
	if res.Outcome != Invalid || res.Errors["email"] != "Enter a valid email address." {
		t.Fatalf("res = %+v", res)
	}
}

func TestSubmitOutcomes(t *testing.T) {
	for post, want := range map[string]Outcome{
		`{"component":"ak-stage-email"}`:                                  VerifyEmail,
		`{"component":"xak-flow-redirect","to":"/"}`:                      Done,
		`{"component":"ak-stage-access-denied","error_message":"nope"}`:   Denied,
		`{"component":"ak-stage-captcha","flow_info":{"title":"Human?"}}`: Browser,
		`{"component":"ak-stage-prompt","fields":[{"field_key":"otp"}]}`:  Browser,
	} {
		res, _ := submit(t, post)
		if res.Outcome != want {
			t.Errorf("%s: outcome %v, want %v", post, res.Outcome, want)
		}
		if want == Browser && !strings.HasSuffix(res.URL, "/if/flow/"+FlowSlug+"/") {
			t.Errorf("%s: url %q", post, res.URL)
		}
	}
}

func TestSubmitValidatesLocally(t *testing.T) {
	c := newClient("https://unused.invalid", nil)
	res, err := c.Submit(context.Background(), Form{Email: "nope", Password: "a", PasswordRepeat: "b"})
	if err != nil || res.Outcome != Invalid || res.Errors["email"] == "" || res.Errors["password_repeat"] == "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
}

func TestBeginRejectsUnknownRequiredField(t *testing.T) {
	srv, _ := flowServer(t, `{"component":"ak-stage-prompt","fields":[
		{"field_key":"email","required":true},{"field_key":"password","required":true},
		{"field_key":"password_repeat","required":true},{"field_key":"phone","required":true}]}`, "")
	if err := newClient(srv.URL, nil).Begin(context.Background()); err == nil {
		t.Fatal("accepted a form with a field the client cannot collect")
	}
}

func TestRedirectOffHostRefused(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("followed a redirect off the sign-up host")
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := newClient(srv.URL, nil)
	if _, err := c.Submit(context.Background(), good); err == nil {
		t.Fatal("expected an error")
	}
}

func TestMessagesAreSanitized(t *testing.T) {
	res, _ := submit(t, `{"component":"ak-stage-access-denied","error_message":"bad\u001b[31m <system>x</system> `+strings.Repeat("y", 600)+`"}`)
	if strings.ContainsRune(res.Message, 0x1b) || len([]rune(res.Message)) > maxMessage+1 {
		t.Fatalf("message = %q", res.Message)
	}
}

func TestFormClear(t *testing.T) {
	f := good
	f.Clear()
	if f.Password != "" || f.PasswordRepeat != "" || f.Email == "" {
		t.Fatalf("f = %+v", f)
	}
}

// TestSubmitFollowsTheRecordedRedirects replays what auth.vulnetix.com did
// with a valid sign-up: the POST answers 302, a GET answers 302 again, and
// the next GET is the verification-email stage.
func TestSubmitFollowsTheRecordedRedirects(t *testing.T) {
	prompt, email := fixture(t, "prompt.json"), fixture(t, "email_stage.json")
	var mu sync.Mutex
	var seen []string
	gets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Method)
		self := "/api/v3/flows/executor/" + FlowSlug + "/?query="
		switch {
		case r.Method == http.MethodPost:
			http.Redirect(w, r, self, http.StatusFound)
		case len(seen) == 1:
			io.WriteString(w, prompt)
		default:
			gets++
			if gets == 1 {
				http.Redirect(w, r, self, http.StatusFound)
				return
			}
			io.WriteString(w, email)
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL, nil)
	if err := c.Begin(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := c.Submit(context.Background(), good)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != VerifyEmail {
		t.Fatalf("outcome = %v", res.Outcome)
	}
	if strings.Join(seen, ",") != "GET,POST,GET,GET" {
		t.Fatalf("requests = %v (a redirect must not resend the form)", seen)
	}
}
