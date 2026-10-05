// Package sessionsync mirrors Belai sessions to the Vulnetix website and
// carries prompts typed there back into the live session.
//
// The host is the source of truth. The Syncer never builds a transcript entry
// of its own: it tails the session's JSONL file and uploads each line with its
// line index (seq), so what the website shows is exactly what is on disk, an
// upload can be retried or repeated without duplicating anything, and a host
// that restarts resumes from the server's high-water mark. A prompt from the
// website is a request (RemotePrompt) that the TUI admits and writes to the
// JSONL like a typed prompt; only then, through the same tail, does it reach
// the website as a transcript line.
//
// Requests go only to the Vulnetix console (https://*.vulnetix.com, or a
// loopback origin for local development) with the Vulnetix CLI's own
// credential in the Authorization header. See docs/session-sync.md.
package sessionsync

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/netguard"
)

// DefaultWebURL is the Vulnetix console origin; $VULNETIX_WEB_URL overrides it
// exactly as it does for the CLI device login.
const DefaultWebURL = "https://www.vulnetix.com"

// apiPath is where the console proxies vdb-site's /v1/belai routes.
const apiPath = "/api/site/v1/belai"

// requestTimeout bounds every call but the inbox long-poll.
const requestTimeout = 20 * time.Second

// ErrNotFound is a 404: the session or host is not the caller's, or is gone.
var ErrNotFound = errors.New("sessionsync: not found")

// ErrUnauthorized is a 401: the credential was refused.
var ErrUnauthorized = errors.New("sessionsync: the Vulnetix credential was refused")

// ErrConflict is a 409: the request no longer applies (a draft cancelled or
// expired on the website, an answer already given).
var ErrConflict = errors.New("sessionsync: the request no longer applies")

// IsConflict reports whether err is ErrConflict.
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }

// BaseURL returns the belai API base for a console origin ("" = default).
func BaseURL(origin string) string {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		origin = DefaultWebURL
	}
	return origin + apiPath
}

// AllowedOrigin reports whether the credential may be sent to rawURL: https on
// vulnetix.com or a subdomain, or any scheme on a loopback host (local dev).
func AllowedOrigin(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Host == "" {
		return false
	}
	if loopback(u) {
		return u.Scheme == "http" || u.Scheme == "https"
	}
	h := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && (h == "vulnetix.com" || strings.HasSuffix(h, ".vulnetix.com"))
}

func loopback(u *url.URL) bool {
	return netguard.IsLoopbackHost(u.Hostname())
}

// errTestOrigin refuses a real origin from a test binary. A developer's
// machine holds a real Vulnetix CLI credential and sync is on by default,
// so a test that forgot to isolate VULNETIX_WEB_URL pushed its fixture
// items ("Survey tests") to the developer's own board on every go test run.
var errTestOrigin = errors.New("sessionsync: a test binary never syncs to a non-loopback origin; set VULNETIX_WEB_URL to a loopback server")

// Client is the thin HTTP client for /v1/belai. AuthHeader is read on every
// request so a re-login takes effect without a restart.
type Client struct {
	Base       string
	AuthHeader func() (string, error)
	HTTP       *http.Client
}

// NewClient validates base and returns a client for it.
func NewClient(base string, auth func() (string, error), hc *http.Client) (*Client, error) {
	if !AllowedOrigin(base) {
		return nil, fmt.Errorf("sessionsync: refusing to send the Vulnetix credential to %s", base)
	}
	if u, _ := url.Parse(base); testing.Testing() && !loopback(u) {
		return nil, errTestOrigin
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{Base: strings.TrimRight(base, "/"), AuthHeader: auth, HTTP: hc}, nil
}

// Host is what the website shows for a machine.
type Host struct {
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	BelaiVersion string `json:"belaiVersion"`
	// RC is sent only by `belai rc`: the directories it will start sessions
	// in and how many it runs at once.
	RC *RCInfo `json:"rc,omitempty"`
}

// RCInfo is a remote-control daemon's advertisement.
type RCInfo struct {
	Dirs        []RCDir `json:"dirs"`
	MaxSessions int     `json:"maxSessions"`
	// MaxWorkers is agents.max_workers; Profiles and Crews are the worker
	// profiles and crews this host can start. Harness facts only: names,
	// lists, labels and limits, never a prompt.
	MaxWorkers int         `json:"maxWorkers"`
	Profiles   []RCProfile `json:"profiles"`
	Crews      []RCCrew    `json:"crews"`
	// Agents are the agent profiles a web-started session can be engaged
	// with (agent mode): names only, never a prompt or a tool list. Nil from
	// an older daemon, which offers no choice.
	Agents []RCAgent `json:"agents,omitempty"`
	// Items are the library items (skills, prompts, ...) this host holds, by
	// kind, name and the hash of their canonical document: facts only, no
	// content, and only for the kinds whose sync switch is on.
	Items []RCItem `json:"items"`
	// Knowledge is the catalogue of this host's knowledge indexes: document
	// facts only (rc knowledge.go), never passage text. Nil from an older daemon.
	Knowledge []RCKnowledge `json:"knowledge,omitempty"`
	// Models is what a web-started session can run on: the model a session
	// gets when the request names none, and the providers this host has
	// credentials for. Names only, never a key or an endpoint. Nil from an
	// older daemon.
	Models *RCModels `json:"models,omitempty"`
	// Controls says web sessions on this host take session controls (belai rc
	// --web-controls), and ControlCatalogue is the table of them: ids,
	// commands, keys and values, from internal/sessionctl. GuardrailsOff says
	// a web session may turn guardrails off (--web-allow-guardrails-off).
	Controls         bool        `json:"controls"`
	GuardrailsOff    bool        `json:"guardrailsOff"`
	ControlCatalogue []RCControl `json:"controlCatalogue,omitempty"`
	// ProjectSettings says the website may read and edit each offered
	// directory's project preferences (--web-project-settings); each RCDir then
	// carries them.
	ProjectSettings bool `json:"projectSettings"`
}

// RCControl is one session control as the website offers it.
type RCControl struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Usage   string   `json:"usage"`
	Keys    []string `json:"keys,omitempty"`
	Values  []string `json:"values,omitempty"`
	Kind    string   `json:"kind"`
}

// RCModels is a host's model advertisement.
type RCModels struct {
	Default   RCModelDefault `json:"default"`
	Providers []RCProvider   `json:"providers"`
}

// RCModelDefault is the provider and model a web session runs on with no
// override. Routed means routing.kind is "routed": the use-case table picks
// the model per turn, and Provider and Model are the main model it falls
// back to.
type RCModelDefault struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Routed   bool   `json:"routed"`
}

// RCProvider is a configured provider and the models it offers a session.
type RCProvider struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

// Caps on one advertisement's model list; the server applies the same caps.
const (
	MaxRCProviders         = 32
	MaxRCModelsPerProvider = 64
)

// RCItem is one library item on the host, as the website lists where an item
// lives: its kind (skill, prompt, ...), its name and the SHA-256 of the
// canonical document the library would store for it.
type RCItem struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// MaxRCItems is how many items one advertisement carries; the server applies
// the same cap.
const MaxRCItems = 1000

// RCKnowledge is one knowledge index on the host, as the Library's Documents
// view lists it: an agent profile's index (Scope "profile", Key its id) or the
// index of a directory the daemon offers (Scope "project", Root its path).
// Facts only: each document's knowledge address, size, SHA-256 and the labels
// and topics the harness tagged it with. No passage text and no index file.
type RCKnowledge struct {
	Scope string           `json:"scope"`
	Key   string           `json:"key,omitempty"`
	Name  string           `json:"name"`
	Root  string           `json:"root,omitempty"`
	Docs  []RCKnowledgeDoc `json:"docs"`
}

// RCKnowledgeDoc is one indexed document's facts.
type RCKnowledgeDoc struct {
	Address string   `json:"address"`
	Bytes   int64    `json:"bytes"`
	SHA256  string   `json:"sha256"`
	Kind    string   `json:"kind,omitempty"`
	Lang    string   `json:"lang,omitempty"`
	Labels  []string `json:"labels"`
	Topics  []string `json:"topics"`
}

// Caps on the knowledge catalogue one advertisement carries; the server
// applies the same caps.
const (
	MaxRCKnowledgeIndexes = 32
	MaxRCKnowledgeDocs    = 256
)

// RCAgent is one agent profile a web session can be started with. The name is
// what the start request carries and the host checks again; DisplayName is how
// the console shows it.
type RCAgent struct {
	Name        string `json:"name"`
	Builtin     bool   `json:"builtin"`
	DisplayName string `json:"displayName,omitempty"`
}

// RCProfile is one worker profile's routing, as the website shows it.
type RCProfile struct {
	Name          string   `json:"name"`
	Builtin       bool     `json:"builtin"`
	Lists         []string `json:"lists"`
	Labels        []string `json:"labels"`
	AssignedOnly  bool     `json:"assignedOnly"`
	OnSuccess     RCRoute  `json:"onSuccess"`
	OnFailure     RCRoute  `json:"onFailure"`
	HandoffTo     []string `json:"handoffTo"`
	HandoffLabels []string `json:"handoffLabels"`
	MaxAttempts   int      `json:"maxAttempts"`
	Lease         string   `json:"lease"`
	MaxWall       string   `json:"maxWall"`
	MaxPasses     int      `json:"maxPasses"`
	// How the console presents the agent (agentprofile identity fields): its
	// UUID, display name, four colours and avatar id. Presentation only; empty
	// for a profile that has none, and none of it is a prompt.
	ID          string   `json:"id,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Palette     []string `json:"palette,omitempty"`
	AvatarID    string   `json:"avatarId,omitempty"`
}

// RCRoute is a destination list plus label edits.
type RCRoute struct {
	List       string   `json:"list"`
	Labels     []string `json:"labels"`
	DropLabels []string `json:"dropLabels"`
}

// RCCrew is one crew and its members. ID is the crew's own uuid (none for a
// built-in or a crew not yet stamped) and Description its one line, so the
// website can tell which library crew this host holds.
type RCCrew struct {
	ID          string     `json:"id,omitempty"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Builtin     bool       `json:"builtin"`
	Members     []RCMember `json:"members"`
}

// RCMember is one crew member and how many workers run it.
type RCMember struct {
	Profile  string `json:"profile"`
	Replicas int    `json:"replicas"`
}

// RCWorker is one live fleet worker on the host (fleet.Record, trimmed).
type RCWorker struct {
	ID      string `json:"id"`
	Profile string `json:"profile"`
	Crew    string `json:"crew,omitempty"`
	State   string `json:"state"`
	Item    string `json:"item,omitempty"`
	Project string `json:"project,omitempty"`
	Session string `json:"session,omitempty"`
	Started int64  `json:"started"`
	// Beat is the worker's last registry write; Stopped and Reason say when
	// and why a recently stopped or failed worker ended. Done and Failed
	// count its items; Branch is its current or last item's branch.
	Beat    int64  `json:"heartbeat,omitempty"`
	Stopped int64  `json:"stopped,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Done    int    `json:"done"`
	Failed  int    `json:"failed"`
	Branch  string `json:"branch,omitempty"`
	// Log is the tail of the worker's own log file, startup included:
	// harness lines and the detached process's stderr, each cleaned by
	// CleanLogLine. Never a transcript: model output goes to the session.
	Log []string `json:"log,omitempty"`
}

// Worker log limits for the heartbeat: a few lines per worker, each
// clipped, and a budget across all workers so a busy host stays small.
const (
	RCWorkerLogLines  = 12
	RCWorkerLogLine   = 240
	RCWorkerLogBudget = 48 << 10
)

// CleanLogLine makes one worker log line safe to send: delimiter markup,
// ANSI, control and bidi runes are removed as for a web prompt, whitespace
// collapses to one line, and it is clipped to RCWorkerLogLine bytes on a
// rune boundary.
func CleanLogLine(s string) string {
	s = strings.Join(strings.Fields(CleanPrompt(ansi.Strip(s))), " ")
	if len(s) <= RCWorkerLogLine {
		return s
	}
	cut := RCWorkerLogLine
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// RCDir is one directory an rc daemon offers. Source is "trusted" (a project
// the host already trusted) or "arg" (passed as `belai rc --dir`).
type RCDir struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Source string `json:"source"`
	// The directory's git facts, when it is a checkout of a forge repository:
	// owner/repo, the forge host, its kind, the checked-out branch and the
	// default branch the clone recorded. Identifier-shaped values only, read
	// from the repository's files (rc dirgit.go); never a URL or a credential.
	Remote        string `json:"remote,omitempty"`
	Host          string `json:"host,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Branch        string `json:"branch,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	// Prefs and Effective are the directory's project preferences, sent only
	// with --web-project-settings: Prefs holds the keys set in the host's
	// preference file for the directory (flat keys, internal/config
	// PrefKeys), Effective each key's resolved value and the settings layer it
	// came from. Values are booleans, fixed words and numbers only.
	Prefs     map[string]any         `json:"prefs,omitempty"`
	Effective map[string]RCEffective `json:"effective,omitempty"`
}

// RCEffective is one preference key's resolved value and its settings layer
// (default, global, project_prefs, project, env, flag).
type RCEffective struct {
	Value  any    `json:"value"`
	Origin string `json:"origin"`
}

// Dispatch is a website request to an rc daemon: start a session in Cwd with
// Prompt, or (Kind "stop") stop SessionID. Everything in it is untrusted: the
// daemon re-checks Cwd against its own list and the prompt goes through the
// same admission as a typed one.
type Dispatch struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Cwd       string `json:"cwd"`
	Mode      string `json:"mode"`
	Prompt    string `json:"prompt"`
	SessionID string `json:"sessionId"`
	// Profile or Crew names what a "worker" or "crew" request starts.
	Profile string `json:"profile,omitempty"`
	Crew    string `json:"crew,omitempty"`
	// Fill makes a "crew" request start only the replicas the crew lacks in
	// the directory (`belai agent start -crew CREW -fill`).
	Fill bool `json:"fill,omitempty"`
	// Worker is the worker id a "pause" or "resume" request names.
	Worker string `json:"worker,omitempty"`
	// A "profile_install" request names a library profile and one of its
	// versions, and says whether it may replace this host's profile of the same
	// name and id. A "profile_backup" request names the host's profile in
	// Profile. Identifiers only: the profile text is fetched, never carried.
	Library   string `json:"library,omitempty"`
	Version   string `json:"version,omitempty"`
	Overwrite bool   `json:"overwrite,omitempty"`
	// A "teleport_code" request names its teleport in Teleport, and says in Push
	// that the target's user agreed to a teleport branch, and in Replay that the
	// forge is not to be tried (the target could not fetch the branch).
	Push   bool `json:"push,omitempty"`
	Replay bool `json:"replay,omitempty"`
	// An "avatar" request names the agent creator whose avatar to draw.
	Creator string `json:"creator,omitempty"`
	// A "crew_install" request names a library crew and version in Library and
	// Version, and Members the library profile versions its members resolve to,
	// which the host installs first. A "crew_backup" request names the host's
	// crew in Crew. Identifiers only.
	Members []CrewMemberRef `json:"members,omitempty"`
	// An "item_backup" request names the host's library item in ItemKind (skill,
	// prompt, ...) and Name. An "item_install" request names a library item and
	// one of its versions in Library and Version, its kind in ItemKind, and says in
	// Overwrite whether it may replace the host's item. ItemKind is not "kind":
	// that field already names the request. Identifiers only: a document is
	// fetched, never carried.
	ItemKind string `json:"itemKind,omitempty"`
	Name     string `json:"name,omitempty"`
	// A "provider_keys_install" request names the catalogue slugs of the providers
	// whose stored keys the host is to take. Slugs only: a key is fetched over TLS,
	// once, never carried.
	Providers []string `json:"providers,omitempty"`
	// A "start" request may name the provider, model and effort the session
	// runs on. Empty means the host's own default (or its routing table).
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	// GitSync, when set on a "start" request, switches the new session's git
	// sync on or off; nil leaves it to the host's settings.
	GitSync *bool `json:"gitSync,omitempty"`
	// A "project_prefs" request names an offered directory in Cwd and the flat
	// preference keys to set (with their values) and to clear. Keys and values
	// are checked by the host against config.ProjectPrefs' shape.
	PrefsSet   map[string]any `json:"prefsSet,omitempty"`
	PrefsUnset []string       `json:"prefsUnset,omitempty"`
	// A "teleport_backup" request names the profile the teleported session ran
	// under in Profiles (the host also backs up the crews that list it and their
	// members) and the teleport it serves in Teleport. Identifiers only.
	Teleport  string   `json:"teleport,omitempty"`
	Profiles  []string `json:"profiles,omitempty"`
	CreatedAt int64    `json:"createdAt"`
}

// CrewMemberRef is one member profile a crew_install request puts on the host:
// the profile name the crew lists and the library version that provides it.
type CrewMemberRef struct {
	Profile string `json:"profile"`
	Library string `json:"library"`
	Version string `json:"version"`
}

// Dispatch outcomes the daemon reports back.
const (
	DispatchStarted = "started"
	DispatchStopped = "stopped"
	DispatchRefused = "refused"
)

// SessionMeta is the session's registration and display metadata.
type SessionMeta struct {
	HostID      string `json:"hostId"`
	ProjectKey  string `json:"projectKey,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Name        string `json:"name,omitempty"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Mode        string `json:"mode,omitempty"`
	// ActiveProfile is the agent profile the session ran under (belai:patcher).
	ActiveProfile   string `json:"activeProfile,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	ResumedFromID   string `json:"resumedFromId,omitempty"`
	RemotePrompts   bool   `json:"remotePrompts"`
	// RemoteAnswers says the host takes web answers to its open asks.
	RemoteAnswers bool `json:"remoteAnswers"`
	// Controls says the host takes session controls from the web (belai rc
	// --web-controls). ControlState is the session's current controls as
	// internal/sessionctl.State, opaque here; the server validates its shape.
	Controls     bool            `json:"controls"`
	ControlState json.RawMessage `json:"controlState,omitempty"`
	// Shell says the host runs a shell line from the web (belai rc
	// --web-shell).
	Shell     bool  `json:"shell"`
	CreatedAt int64 `json:"createdAt,omitempty"`
	// DispatchID is the website request that started this session on an rc
	// daemon; empty for a session someone started at the terminal.
	DispatchID string `json:"dispatchId,omitempty"`
	// Git is the session's repository as internal/gitsync.Info (branch,
	// worktree, distance from main, pull request, last sync). Opaque here;
	// the server validates it. Empty outside a repository.
	Git json.RawMessage `json:"git,omitempty"`
}

// Entry is one JSONL line as uploaded: the session.Entry fields plus seq.
type Entry struct {
	Seq        int64           `json:"seq"`
	ID         string          `json:"id"`
	ParentID   string          `json:"parentId,omitempty"`
	Type       string          `json:"type"`
	Role       string          `json:"role,omitempty"`
	Content    string          `json:"content,omitempty"`
	Timestamp  int64           `json:"timestamp"`
	Meta       json.RawMessage `json:"meta,omitempty"`
	SubagentID string          `json:"subagent_id,omitempty"`
}

// RemotePrompt is a prompt typed on the website, claimed from the inbox.
type RemotePrompt struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"createdAt"`
	// Origin names a prompt the host raised itself (a failed shell line to
	// analyse) rather than one that came from the web. It is never on the wire.
	Origin string `json:"-"`
}

// RemoteAnswer is a web answer to a question the host asked, claimed from the
// inbox. AskID is the host's ask entry id; Payload is untrusted JSON the host
// validates against the ask that is actually open (see the TUI's
// handleRemoteAnswer).
type RemoteAnswer struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	AskID     string          `json:"askId"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"createdAt"`
}

// RemoteDraft is a request from the website's agent builder to draft an agent
// profile from a premise, claimed from the inbox. The premise is untrusted
// web text: the host cleans it and admits it like a web prompt before the
// drafter sees it. Context carries harness facts only (worker names, board
// labels), which the drafter re-checks.
type RemoteDraft struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Premise   string `json:"premise"`
	Context   struct {
		Workers []string `json:"workers"`
		Labels  []string `json:"labels"`
	} `json:"context"`
	CreatedAt int64 `json:"createdAt"`
	ExpiresAt int64 `json:"expiresAt"`
}

// RemoteCommand is a request sent from the website, claimed from the inbox: a
// session control (a slash line "/caveman on" or a key "f4") or, from a host
// run with --web-shell, one shell line; exactly one of Line, Key and Shell.
// It is untrusted: a control is parsed by internal/sessionctl, which knows
// only its fixed controls, and a shell line runs only after the TUI's own
// permission rules and under its sandbox profile (internal/rc shell.go).
type RemoteCommand struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Line      string `json:"line,omitempty"`
	Key       string `json:"key,omitempty"`
	// Shell is the line to run, Cwd the directory the page was showing and
	// Attach whether its output may go to the model's next turn (the
	// composer's `!cmd`; the console's remote shell never attaches).
	Shell     string `json:"shell,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Attach    bool   `json:"attach,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

// Prompt outcomes the host reports back.
const (
	AckQueued   = "queued"
	AckAccepted = "accepted"
	AckRefused  = "refused"
)

// gzipMinBytes is the body size from which an entry upload is compressed.
// Diffs make lines large and compress well; small batches are not worth it.
const gzipMinBytes = 8 << 10

func (c *Client) do(ctx context.Context, method, path string, in, out any, timeout time.Duration) error {
	return c.doBody(ctx, method, path, in, out, timeout, false)
}

func (c *Client) doBody(ctx context.Context, method, path string, in, out any, timeout time.Duration, compress bool) error {
	status, data, err := c.roundTrip(ctx, method, path, in, timeout, compress, defaultMaxBody)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusUnauthorized:
		return ErrUnauthorized
	case status == http.StatusConflict:
		return ErrConflict
	case status < 200 || status > 299:
		return fmt.Errorf("sessionsync: %s %s: HTTP %d", method, path, status)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// defaultMaxBody caps a response the client reads.
const defaultMaxBody = 4 << 20

// roundTrip sends one authenticated request and returns the status and the body
// (read up to limit bytes). It maps no status to an error: callers differ in how
// they read one, and teleport reads the reason a 409 carries.
func (c *Client) roundTrip(ctx context.Context, method, path string, in any, timeout time.Duration, compress bool, limit int64) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	gzipped := false
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, nil, err
		}
		if compress && len(b) >= gzipMinBytes {
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			if _, err := zw.Write(b); err == nil && zw.Close() == nil {
				b, gzipped = buf.Bytes(), true
			}
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return 0, nil, err
	}
	auth, err := c.AuthHeader()
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if gzipped {
		req.Header.Set("Content-Encoding", "gzip")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return resp.StatusCode, data, nil
}

// PutHost registers or refreshes this machine.
func (c *Client) PutHost(ctx context.Context, hostID string, h Host) error {
	return c.do(ctx, http.MethodPut, "/hosts/"+url.PathEscape(hostID), h, nil, requestTimeout)
}

// PutSession registers the session as live and returns the server's lastSeq
// (-1 when it holds no lines yet).
func (c *Client) PutSession(ctx context.Context, sessionID string, m SessionMeta) (int64, error) {
	last, _, err := c.PutSessionControls(ctx, sessionID, m)
	return last, err
}

// Controls are the per-session settings the website changed and the host has
// not yet confirmed. Nil fields are untouched. The server sends one only until
// the host's next report shows it applied, so a later local change is not
// overridden by an old web one.
type Controls struct {
	// GitSync switches the session's git sync (internal/gitsync) on or off.
	GitSync *bool `json:"gitSync,omitempty"`
}

// PutSessionControls is PutSession that also returns the website's pending
// controls for the session.
func (c *Client) PutSessionControls(ctx context.Context, sessionID string, m SessionMeta) (int64, Controls, error) {
	var out struct {
		LastSeq  int64    `json:"lastSeq"`
		Controls Controls `json:"controls"`
	}
	err := c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(sessionID), m, &out, requestTimeout)
	return out.LastSeq, out.Controls, err
}

// PostEntries uploads lines; repeats are ignored server-side.
func (c *Client) PostEntries(ctx context.Context, sessionID string, entries []Entry) (int64, error) {
	var out struct {
		LastSeq int64 `json:"lastSeq"`
	}
	err := c.doBody(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/entries",
		map[string]any{"entries": entries}, &out, requestTimeout, true)
	return out.LastSeq, err
}

// Heartbeat keeps the session live.
func (c *Client) Heartbeat(ctx context.Context, sessionID string) error {
	_, err := c.HeartbeatControls(ctx, sessionID)
	return err
}

// HeartbeatControls is Heartbeat that also returns the website's pending
// controls for the session.
func (c *Client) HeartbeatControls(ctx context.Context, sessionID string) (Controls, error) {
	var out struct {
		Controls Controls `json:"controls"`
	}
	err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/heartbeat", nil, &out, requestTimeout)
	return out.Controls, err
}

// End moves the session into History.
func (c *Client) End(ctx context.Context, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/end", nil, nil, requestTimeout)
}

// Inbox long-polls for web prompts and web answers addressed to sessionID, a
// live session of this host. A server that predates answers sends none; one
// that predates the session filter hands over the whole host's inbox, which
// the TUI refuses per prompt as before.
func (c *Client) Inbox(ctx context.Context, hostID, sessionID string, wait time.Duration) ([]RemotePrompt, []RemoteAnswer, []RemoteDraft, error) {
	b, err := c.InboxBatch(ctx, hostID, sessionID, wait)
	return b.Prompts, b.Answers, b.Drafts, err
}

// Inbox is everything one inbox poll returned.
type Inbox struct {
	Prompts  []RemotePrompt  `json:"prompts"`
	Answers  []RemoteAnswer  `json:"answers"`
	Drafts   []RemoteDraft   `json:"drafts"`
	Commands []RemoteCommand `json:"commands"`
}

// InboxBatch is Inbox with session controls. A server that predates them
// sends none.
func (c *Client) InboxBatch(ctx context.Context, hostID, sessionID string, wait time.Duration) (Inbox, error) {
	var out Inbox
	path := fmt.Sprintf("/hosts/%s/inbox?wait=%d", url.PathEscape(hostID), int(wait/time.Second))
	if sessionID != "" {
		path += "&session=" + url.QueryEscape(sessionID)
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out, wait+requestTimeout)
	return out, err
}

// AckCommand reports what the host did with a session control: accepted, with
// the session's new controls, or refused, with a harness-worded reason.
func (c *Client) AckCommand(ctx context.Context, commandID, status, reason string, state json.RawMessage) error {
	body := map[string]any{"status": status, "reason": reason}
	if len(state) > 0 {
		body["state"] = state
	}
	return c.do(ctx, http.MethodPost, "/commands/"+url.PathEscape(commandID)+"/ack", body, nil, requestTimeout)
}

// Draft outcomes the host reports back.
const (
	DraftDone    = "done"
	DraftRefused = "refused"
)

// DraftResult reports an agent draft: done with the result (field and crew
// offers), or refused with a reason. The server refuses a result for a draft
// that was cancelled, expired or already answered.
func (c *Client) DraftResult(ctx context.Context, draftID, status, reason string, result any) error {
	body := map[string]any{"status": status, "reason": reason}
	if status == DraftDone {
		body["result"] = result
	}
	return c.do(ctx, http.MethodPost, "/agent-drafts/"+url.PathEscape(draftID)+"/result", body, nil, requestTimeout)
}

// AckAnswer reports what the host did with a web answer: accepted (applied,
// with the ask_answer entry id) or refused (with a reason).
func (c *Client) AckAnswer(ctx context.Context, answerID, status, reason, entryID string) error {
	return c.do(ctx, http.MethodPost, "/answers/"+url.PathEscape(answerID)+"/ack",
		map[string]string{"status": status, "reason": reason, "entryId": entryID}, nil, requestTimeout)
}

// Ack reports what the host did with a web prompt.
func (c *Client) Ack(ctx context.Context, promptID, status, reason, entryID string) error {
	return c.do(ctx, http.MethodPost, "/prompts/"+url.PathEscape(promptID)+"/ack",
		map[string]string{"status": status, "reason": reason, "entryId": entryID}, nil, requestTimeout)
}

// RCHeartbeat keeps an rc daemon online and reports how many sessions it runs.
func (c *Client) RCHeartbeat(ctx context.Context, hostID string, running int, workers []RCWorker) error {
	if workers == nil {
		workers = []RCWorker{}
	}
	return c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/rc/heartbeat",
		map[string]any{"running": running, "workers": workers}, nil, requestTimeout)
}

// RCOffline marks the rc daemon stopped; requests still waiting expire.
func (c *Client) RCOffline(ctx context.Context, hostID string) error {
	return c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/rc/offline", nil, nil, requestTimeout)
}

// Dispatches long-polls for website requests to this rc daemon.
func (c *Client) Dispatches(ctx context.Context, hostID string, wait time.Duration) ([]Dispatch, error) {
	var out struct {
		Dispatches []Dispatch `json:"dispatches"`
	}
	path := fmt.Sprintf("/hosts/%s/dispatch?wait=%d", url.PathEscape(hostID), int(wait/time.Second))
	err := c.do(ctx, http.MethodGet, path, nil, &out, wait+requestTimeout)
	return out.Dispatches, err
}

// AckDispatch reports what the daemon did with a request: started (with the
// session id it minted), stopped, or refused (with a reason).
func (c *Client) AckDispatch(ctx context.Context, dispatchID, status, sessionID, reason string) error {
	return c.do(ctx, http.MethodPost, "/dispatches/"+url.PathEscape(dispatchID)+"/ack",
		map[string]string{"status": status, "sessionId": sessionID, "reason": reason}, nil, requestTimeout)
}
