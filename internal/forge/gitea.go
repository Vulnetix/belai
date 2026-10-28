package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

// gitea drives the Gitea API (/api/v1), which Forgejo serves too. There is no
// CLI to drive: tea has no checks and no per-branch pull request view, so the
// provider talks to the API with the token the user gave for that host.
//
// What Gitea lacks is filled in as the vdb-site forge client does it: a
// draft is the "WIP: " title prefix, and checks are the head commit's
// statuses (Gitea and Forgejo Actions, Woodpecker and Drone report there).
type gitea struct {
	kind  string // KindGitea or KindForgejo
	rem   Remote
	base  string // https://host/api/v1
	token string
}

// GiteaHTTP is the client the provider uses; tests replace it.
var GiteaHTTP = &http.Client{Timeout: WriteTimeout}

// Getenv reads a token variable: a live one the secrets vault granted this
// machine first, then the process environment. Tests replace it.
var Getenv = func(name string) string {
	for _, kv := range vaultenv.Default.Environ(time.Now()) {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name && v != "" {
			return v
		}
	}
	return os.Getenv(name)
}

var giteaWIPRE = regexp.MustCompile(`(?i)^\s*(wip:|\[wip\])\s*`)

// tokenEnvFor is the environment variables a token for host is read from,
// in order: one named for the host, then the kind's own.
func tokenEnvFor(kind, host string) []string {
	names := []string{"BELAI_FORGE_TOKEN_" + hostEnv(host)}
	switch kind {
	case KindForgejo:
		names = append(names, "FORGEJO_TOKEN", "GITEA_TOKEN")
	case KindGitea:
		names = append(names, "GITEA_TOKEN")
	case KindGitLab:
		names = append(names, "GITLAB_TOKEN")
	}
	return names
}

// hostEnv is host as an environment variable suffix: upper-cased, every
// character but a letter or a digit an underscore ("codeberg.org" is
// CODEBERG_ORG).
func hostEnv(host string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(host) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// ForgeToken is the token for a Gitea, Forgejo or GitLab host, and the
// variable it came from; "" when none is set.
func ForgeToken(kind, host string) (token, from string) {
	for _, n := range tokenEnvFor(kind, host) {
		if v := strings.TrimSpace(Getenv(n)); v != "" {
			return v, n
		}
	}
	return "", ""
}

func newGitea(rem Remote) (Provider, string) {
	tok, _ := ForgeToken(rem.Kind, rem.Host)
	if tok == "" {
		names := tokenEnvFor(rem.Kind, rem.Host)
		return nil, "no token for " + rem.Host + ": set " + strings.Join(names, " or ")
	}
	// An https or http origin names the web host and port the API is on;
	// an ssh origin's port is ssh's, so the API is taken to be on https.
	scheme, apiHost := "https", rem.Host
	if u, err := url.Parse(rem.URL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		scheme, apiHost = u.Scheme, u.Host
	}
	return gitea{kind: rem.Kind, rem: rem, base: scheme + "://" + apiHost + "/api/v1", token: tok}, ""
}

func (g gitea) Kind() string { return g.kind }
func (gitea) Noun() string   { return "PR" }
func (g gitea) Label() string {
	if g.kind == KindForgejo {
		return "Forgejo (API)"
	}
	return "Gitea (API)"
}

func (g gitea) repoPath() string {
	return "/repos/" + url.PathEscape(g.rem.Owner) + "/" + url.PathEscape(g.rem.Repo)
}

func (g gitea) do(ctx context.Context, timeout time.Duration, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+g.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := GiteaHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", g.rem.Host, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &m)
		msg := Clean(m.Message)
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &giteaError{Status: resp.StatusCode, Msg: msg}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s: unreadable answer", g.rem.Host)
		}
	}
	return nil
}

type giteaError struct {
	Status int
	Msg    string
}

func (e *giteaError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Msg) }

type giteaPull struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	Merged  bool   `json:"merged"`
	Head    struct {
		Ref    string `json:"ref"`
		Sha    string `json:"sha"`
		RepoID int64  `json:"repo_id"`
	} `json:"head"`
	Base struct {
		RepoID int64 `json:"repo_id"`
	} `json:"base"`
}

func (p giteaPull) pr() PR {
	st := strings.ToLower(Clean(p.State))
	if p.Merged {
		st = "merged"
	}
	return PR{Number: p.Number, Title: Clean(p.Title), State: st, URL: Clean(p.HTMLURL),
		Draft: p.Draft || giteaWIPRE.MatchString(p.Title), Branch: Clean(p.Head.Ref)}
}

// openPulls lists the open pull requests whose head is in this repository.
func (g gitea) openPulls(ctx context.Context) ([]giteaPull, error) {
	var all []giteaPull
	for page := 1; page <= 10; page++ {
		var list []giteaPull
		if err := g.do(ctx, WriteTimeout, http.MethodGet, fmt.Sprintf("%s/pulls?state=open&limit=50&page=%d", g.repoPath(), page), nil, &list); err != nil {
			return nil, err
		}
		for _, p := range list {
			if p.Head.RepoID == 0 || p.Head.RepoID == p.Base.RepoID {
				all = append(all, p)
			}
		}
		if len(list) < 50 {
			break
		}
	}
	return all, nil
}

func (g gitea) PRForBranch(ctx context.Context, _, branch string) (*PR, error) {
	if err := argSafe(branch); err != nil {
		return nil, err
	}
	pulls, err := g.openPulls(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range pulls {
		if p.Head.Ref == branch {
			pr := p.pr()
			return &pr, nil
		}
	}
	return nil, nil
}

func (g gitea) CreatePR(ctx context.Context, _ string, a CreatePRArgs) (string, error) {
	if err := argSafe(a.Branch); err != nil {
		return "", err
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.do(ctx, WriteTimeout, http.MethodGet, g.repoPath(), nil, &repo); err != nil {
		return "", err
	}
	if repo.DefaultBranch == "" {
		return "", errors.New(g.rem.Host + ": the repository has no default branch")
	}
	title := a.Title
	if a.Draft && !giteaWIPRE.MatchString(title) {
		title = "WIP: " + title
	}
	var out giteaPull
	err := g.do(ctx, WriteTimeout, http.MethodPost, g.repoPath()+"/pulls",
		map[string]string{"head": a.Branch, "base": repo.DefaultBranch, "title": title, "body": a.Body}, &out)
	var ge *giteaError
	if errors.As(err, &ge) && ge.Status == http.StatusConflict {
		if pr, perr := g.PRForBranch(ctx, "", a.Branch); perr == nil && pr != nil {
			return pr.URL, nil
		}
	}
	if err != nil {
		return "", err
	}
	return Clean(out.HTMLURL), nil
}

func (g gitea) OpenPRsWithPrefix(ctx context.Context, _, prefix string) ([]PR, error) {
	if err := argSafe(prefix); err != nil {
		return nil, err
	}
	pulls, err := g.openPulls(ctx)
	if err != nil {
		return nil, err
	}
	prs := make([]PR, 0, len(pulls))
	for _, p := range pulls {
		prs = append(prs, p.pr())
	}
	return withPrefix(prs, prefix), nil
}

func (g gitea) ClosePR(ctx context.Context, dir string, number int, comment string) error {
	if number <= 0 {
		return errors.New("no pull request number")
	}
	if err := g.CommentPR(ctx, dir, number, comment); err != nil {
		return err
	}
	return g.do(ctx, WriteTimeout, http.MethodPatch, g.repoPath()+"/pulls/"+strconv.Itoa(number), map[string]string{"state": "closed"}, nil)
}

func (g gitea) CommentPR(ctx context.Context, _ string, number int, body string) error {
	if number <= 0 {
		return errors.New("no pull request number")
	}
	return g.do(ctx, WriteTimeout, http.MethodPost, g.repoPath()+"/issues/"+strconv.Itoa(number)+"/comments", map[string]string{"body": body}, nil)
}

func (g gitea) Checks(ctx context.Context, _, branch string, _ PR) ([]Check, error) {
	if err := argSafe(branch); err != nil {
		return nil, err
	}
	var out struct {
		Statuses []struct {
			Status    string `json:"status"`
			State     string `json:"state"`
			Context   string `json:"context"`
			TargetURL string `json:"target_url"`
		} `json:"statuses"`
	}
	if err := g.do(ctx, ReadTimeout, http.MethodGet, g.repoPath()+"/commits/"+escapeRef(branch)+"/status?limit=100", nil, &out); err != nil {
		return nil, err
	}
	checks := make([]Check, 0, len(out.Statuses))
	for _, s := range out.Statuses {
		st := s.Status
		if st == "" {
			st = s.State
		}
		checks = append(checks, Check{Name: Clean(s.Context), State: normState(st), Link: Clean(s.TargetURL)})
	}
	return checks, nil
}

// escapeRef escapes a branch name segment by segment: Gitea's ref routes take
// the slashes of a branch like belai/K-12 as they are.
func escapeRef(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
