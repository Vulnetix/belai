// Package deciderserver finds or starts the one local server that answers
// decision requests with Strands Decider-2B: upstream's own
// `strands-decider serve`, which speaks the /v1/systemone API Belai already
// uses for TypeSafe and self-hosted Jev servers. Every belai process on the
// machine shares it, as they share the decision-local llama-server.
//
// The rules:
//   - A running server is used only when it is on loopback and its /health
//     names a Strands Decider checkpoint. That is also how a server the user
//     runs (upstream's on its default port, or any other runtime that serves
//     the API and identifies itself the same way) is found.
//   - Weights are never downloaded here. Download places a pinned checkpoint
//     and a pinned base snapshot under Belai's models directory, checking each
//     file against its pinned SHA-256 (and the Hub's report for LFS files),
//     and only the /model test calls it, after the user confirms the size.
//   - The server reads that snapshot offline: HF_HOME points at Belai's copy
//     and HF_HUB_OFFLINE is set, so it never fetches anything itself.
//   - The argv is harness-fixed (Args): the checkpoint directory, the loopback
//     host, the port and the served name. The binary comes from PATH. The
//     server runs with the scrubbed environment in its own process group.
//   - Launching takes a file lock and probes again, so two processes never
//     start two servers, and only the process that launched a server stops it.
package deciderserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/activity"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/decisionserver"
	"github.com/vulnetix/belai/internal/localinfer"
	"github.com/vulnetix/belai/internal/proc"
)

// DefaultPort is the port a first launch tries; a busy port moves the server
// to a free one, and the state file records where it went.
const DefaultPort = 18098

// UpstreamPort is the port `strands-decider serve` binds when it is not
// told otherwise, where a server the user started is looked for.
const UpstreamPort = 8000

// BinaryName is the upstream command.
const BinaryName = "strands-decider"

// ActivityLabel is the activity-registry label of the decider server.
const ActivityLabel = "strands-decider"

// InstallHint is the harness text that tells the user how to get the server.
const InstallHint = "uv tool install strands-decider (or pip install strands-decider)"

var (
	// ErrWeightsMissing means the checkpoint or base files are not on disk.
	ErrWeightsMissing = errors.New("Strands Decider-2B weights are not downloaded")
	// ErrNoBinary means strands-decider is not on PATH.
	ErrNoBinary = errors.New("strands-decider is not installed")
)

// Options shapes a launch.
type Options struct {
	Registry *activity.Registry
	OnLine   func(string)
	// Deadline bounds the wait for the model to load; zero uses 5 minutes
	// (the first CPU load of a 2B model reads 4.5 GB).
	Deadline time.Duration
	Client   *http.Client
	// Root overrides the models directory (tests).
	Root string
	// Ports overrides where an already-running server is looked for
	// (tests); nil means the recorded port, then UpstreamPort.
	Ports []int
}

// Handle is a server this process can use.
type Handle struct {
	BaseURL string
	Port    int
	// Owned is set when this process launched the server; only then does
	// Stop stop it.
	Owned bool
	stop  func() error
}

// Stop stops the server if this process launched it. It is safe to call on
// a borrowed handle (a no-op) and more than once.
func (h *Handle) Stop() {
	if h != nil && h.Owned && h.stop != nil {
		_ = h.stop()
	}
}

// Root is where Belai keeps the decider's files:
// <models dir>/strands-decider.
func Root() (string, error) {
	dir, err := localinfer.ModelsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "strands-decider"), nil
}

func root(o Options) (string, error) {
	if o.Root != "" {
		return o.Root, nil
	}
	return Root()
}

// CheckpointDir is the directory the server is given as its checkpoint.
func CheckpointDir(rootDir string, m decisions.DeciderModel) string {
	return filepath.Join(rootDir, m.ID, m.Revision)
}

// HFHome is the Hugging Face home the server reads the base model from.
func HFHome(rootDir string) string { return filepath.Join(rootDir, "hf") }

// baseSnapshot is the hub-cache snapshot directory of the base model.
func baseSnapshot(rootDir string, m decisions.DeciderModel) string {
	return filepath.Join(baseRepoDir(rootDir, m), "snapshots", m.BaseRevision)
}

func baseRepoDir(rootDir string, m decisions.DeciderModel) string {
	return filepath.Join(HFHome(rootDir), "hub", "models--"+strings.ReplaceAll(m.BaseRepo, "/", "--"))
}

// placed is one file and where it goes.
type placed struct {
	repo, revision string
	file           decisions.DeciderFile
	dest           string
}

func layout(rootDir string, m decisions.DeciderModel) []placed {
	var out []placed
	ck := CheckpointDir(rootDir, m)
	for _, f := range m.Checkpoint {
		out = append(out, placed{m.Repo, m.Revision, f, filepath.Join(ck, filepath.FromSlash(f.Path))})
	}
	snap := baseSnapshot(rootDir, m)
	for _, f := range m.Base {
		out = append(out, placed{m.BaseRepo, m.BaseRevision, f, filepath.Join(snap, filepath.FromSlash(f.Path))})
	}
	return out
}

// Present reports whether every file of m is on disk at its pinned size.
// The SHA-256 was checked when the file was downloaded.
func Present(rootDir string, m decisions.DeciderModel) bool {
	for _, p := range layout(rootDir, m) {
		fi, err := os.Lstat(p.dest)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != p.file.Size {
			return false
		}
	}
	ref, err := os.ReadFile(filepath.Join(baseRepoDir(rootDir, m), "refs", "main"))
	return err == nil && strings.TrimSpace(string(ref)) == m.BaseRevision
}

// Missing is the bytes still to download for m.
func Missing(rootDir string, m decisions.DeciderModel) int64 {
	var n int64
	for _, p := range layout(rootDir, m) {
		if fi, err := os.Lstat(p.dest); err != nil || !fi.Mode().IsRegular() || fi.Size() != p.file.Size {
			n += p.file.Size
		}
	}
	return n
}

// Download fetches every file of m that is not yet in place, from the
// pinned revisions, into Belai's models directory. Each file must match its
// pinned SHA-256; an LFS file must also match the SHA-256 the Hub reports, so
// a changed upstream file is refused either way. The token, when set, is sent
// to Hugging Face only. Only the /model test calls this, after the user
// confirmed the size.
func Download(ctx context.Context, client *http.Client, rootDir string, m decisions.DeciderModel, token string, progress localinfer.Progress) error {
	all := layout(rootDir, m)
	var total, done int64
	for _, p := range all {
		total += p.file.Size
	}
	for _, p := range all {
		if fi, err := os.Lstat(p.dest); err == nil && fi.Mode().IsRegular() && fi.Size() == p.file.Size {
			done += p.file.Size
			continue
		}
		if p.file.LFS {
			rf, err := localinfer.RemoteInfoAt(ctx, client, p.repo, p.revision, p.file.Path, token)
			if err != nil {
				return err
			}
			if rf.SHA256 != p.file.SHA256 {
				return &localinfer.DownloadError{Class: localinfer.DownloadChecksum, Msg: "Hugging Face reports a different SHA-256 for " + p.file.Path + " than Belai pins"}
			}
		}
		base := done
		var report localinfer.Progress
		if progress != nil {
			report = func(d, _ int64) { progress(base+d, total) }
		}
		want := localinfer.RemoteFile{Size: p.file.Size, SHA256: p.file.SHA256}
		if _, err := localinfer.DownloadTo(ctx, client, p.repo, p.revision, p.file.Path, p.dest, token, want, report); err != nil {
			return err
		}
		done += p.file.Size
	}
	refs := filepath.Join(baseRepoDir(rootDir, m), "refs")
	if err := os.MkdirAll(refs, 0o755); err != nil {
		return &localinfer.DownloadError{Class: localinfer.DownloadDisk, Msg: "create the base model's refs", Err: err}
	}
	if err := os.WriteFile(filepath.Join(refs, "main"), []byte(m.BaseRevision), 0o644); err != nil {
		return &localinfer.DownloadError{Class: localinfer.DownloadDisk, Msg: "record the base model's revision", Err: err}
	}
	return nil
}

// Args is the harness-fixed argv of the server: the checkpoint directory,
// the loopback host, the port and the name it reports. The device is left to
// the server's own detection (CUDA, then Apple's GPU, then the CPU).
func Args(checkpointDir string, port int, m decisions.DeciderModel) []string {
	return []string{"serve", checkpointDir, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--model-name", "strands-" + m.ID}
}

// Env is the harness-fixed environment added to the scrubbed one: the
// server reads the base model from Belai's snapshot and never goes online.
func Env(rootDir string) []string {
	return []string{"HF_HOME=" + HFHome(rootDir), "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_HUB_DISABLE_TELEMETRY=1"}
}

var (
	binMu    sync.Mutex
	binCache = map[string]binEntry{}
)

type binEntry struct {
	mod time.Time
	ok  bool
}

// Binary returns strands-decider from PATH. A program by that name is
// accepted only when its help text names the decider, because another
// program would otherwise be handed serve arguments. The check is cached per
// path and modification time: the CLI imports torch, so --help is slow.
func Binary() (localinfer.Binary, bool) {
	p, err := exec.LookPath(BinaryName)
	if err != nil {
		return localinfer.Binary{}, false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return localinfer.Binary{}, false
	}
	binMu.Lock()
	e, ok := binCache[p]
	binMu.Unlock()
	if !ok || !e.mod.Equal(fi.ModTime()) {
		e = binEntry{mod: fi.ModTime(), ok: isDeciderCLI(p)}
		binMu.Lock()
		binCache[p] = e
		binMu.Unlock()
	}
	if !e.ok {
		return localinfer.Binary{}, false
	}
	return localinfer.Binary{Name: BinaryName, Path: p}, true
}

func isDeciderCLI(p string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "--help")
	cmd.Env = proc.ScrubbedEnv()
	proc.SetProcessGroup(cmd)
	out, _ := cmd.CombinedOutput()
	lower := strings.ToLower(string(out))
	return strings.Contains(lower, "serve") && (strings.Contains(lower, "decider") || strings.Contains(lower, "systemone"))
}

// Health is what a decider server says about itself, reduced to the facts
// Belai uses.
type Health struct {
	Model      string
	Checkpoint string
	Device     string
}

// Probe asks base's /health whether it is a Strands Decider server. base
// must be a loopback URL; anything else is refused without a request.
func Probe(ctx context.Context, client *http.Client, base string) (Health, bool) {
	if !loopbackURL(base) {
		return Health{}, false
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/health", nil)
	if err != nil {
		return Health{}, false
	}
	resp, err := c.Do(req)
	if err != nil {
		return Health{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{}, false
	}
	return ParseHealth(io.LimitReader(resp.Body, 64<<10))
}

// ParseHealth reads a decider /health body. It is a decider only when the
// status is ok and its checkpoint, base model or served name identifies a
// Strands Decider checkpoint. Every string kept is reduced to identifier
// characters.
func ParseHealth(r io.Reader) (Health, bool) {
	var h struct {
		Status     string `json:"status"`
		Model      string `json:"model"`
		Checkpoint string `json:"checkpoint"`
		Device     string `json:"device"`
	}
	if json.NewDecoder(r).Decode(&h) != nil || h.Status != "ok" {
		return Health{}, false
	}
	if !decisions.IsDeciderID(h.Model) && !decisions.IsDeciderID(h.Checkpoint) && !ownCheckpoint(h.Checkpoint) {
		return Health{}, false
	}
	return Health{Model: ident(h.Model), Checkpoint: ident(filepath.Base(h.Checkpoint)), Device: ident(h.Device)}, true
}

// ownCheckpoint reports whether path is a checkpoint directory Belai placed.
func ownCheckpoint(path string) bool {
	for _, m := range decisions.DeciderModels {
		if strings.HasSuffix(filepath.ToSlash(path), "/"+m.ID+"/"+m.Revision) {
			return true
		}
	}
	return false
}

func ident(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == ':':
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

func loopbackURL(base string) bool {
	for _, p := range []string{"http://127.0.0.1:", "http://localhost:", "http://[::1]:"} {
		if strings.HasPrefix(base, p) {
			return true
		}
	}
	return false
}

func rootURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

// state is the on-disk record of the running server.
type state struct {
	Port  int    `json:"port"`
	Model string `json:"model"`
}

const stateFile = "strands-decider.json"

func readState() (state, bool) {
	dir, err := decisionserver.RunDir()
	if err != nil {
		return state{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, stateFile))
	if err != nil {
		return state{}, false
	}
	var s state
	if json.Unmarshal(b, &s) != nil || s.Port <= 0 || s.Port > 65535 {
		return state{}, false
	}
	return s, true
}

func writeState(s state) {
	dir, err := decisionserver.RunDir()
	if err != nil {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	b, _ := json.Marshal(s)
	_ = os.WriteFile(filepath.Join(dir, stateFile), b, 0o600)
}

// BaseURL is where the decider server is expected: the recorded port, else
// DefaultPort. It never launches anything.
func BaseURL() string {
	if s, ok := readState(); ok {
		return rootURL(s.Port)
	}
	return rootURL(DefaultPort)
}

func ports(o Options) []int {
	if o.Ports != nil {
		return o.Ports
	}
	var out []int
	if s, ok := readState(); ok {
		out = append(out, s.Port)
	}
	out = append(out, DefaultPort, UpstreamPort)
	return out
}

// Running looks for a decider server already answering on loopback: the
// recorded port, Belai's default and upstream's default. It never launches.
func Running(ctx context.Context, o Options) (string, Health, bool) {
	seen := map[int]bool{}
	for _, p := range ports(o) {
		if seen[p] {
			continue
		}
		seen[p] = true
		base := rootURL(p)
		if h, ok := Probe(ctx, o.Client, base); ok {
			return base, h, true
		}
	}
	return "", Health{}, false
}

// Ensure returns a handle to a server answering with m: a running one when
// there is one, else one launched from the downloaded weights.
func Ensure(ctx context.Context, m decisions.DeciderModel, o Options) (*Handle, error) {
	if base, _, ok := Running(ctx, o); ok {
		port, _ := strconv.Atoi(base[strings.LastIndexByte(base, ':')+1:])
		return &Handle{BaseURL: base, Port: port}, nil
	}
	rootDir, err := root(o)
	if err != nil {
		return nil, err
	}
	if !Present(rootDir, m) {
		return nil, ErrWeightsMissing
	}
	bin, ok := Binary()
	if !ok {
		return nil, ErrNoBinary
	}
	dir, err := decisionserver.RunDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	release, err := config.AcquireFileLock(filepath.Join(dir, "strands-decider.lock"))
	if err != nil {
		return nil, fmt.Errorf("decider server lock: %w", err)
	}
	defer release()
	// Another process may have launched it while this one waited.
	if base, _, ok := Running(ctx, o); ok {
		port, _ := strconv.Atoi(base[strings.LastIndexByte(base, ':')+1:])
		return &Handle{BaseURL: base, Port: port}, nil
	}

	port := DefaultPort
	if s, ok := readState(); ok {
		port = s.Port
	}
	if !decisionserver.PortFree(port) {
		if port, err = localinfer.FreePort(); err != nil {
			return nil, err
		}
	}
	deadline := o.Deadline
	if deadline <= 0 {
		deadline = 5 * time.Minute
	}
	stop, err := localinfer.Launch(ctx, bin, Args(CheckpointDir(rootDir, m), port, m), localinfer.BaseURL("127.0.0.1", port), localinfer.LaunchOptions{
		Deadline: deadline,
		Env:      Env(rootDir),
		Registry: o.Registry,
		Label:    ActivityLabel,
		Pidfile:  filepath.Join(dir, "strands-decider.pid"),
		OnLine:   o.OnLine,
		OnPort:   func(p int) { port = p },
	})
	if err != nil {
		return nil, err
	}
	writeState(state{Port: port, Model: m.ID})
	return &Handle{BaseURL: rootURL(port), Port: port, Owned: true, stop: stop}, nil
}

// Status is what /providers and /model show about the decider on this
// machine. Every field is a harness fact.
type Status struct {
	Installed bool
	Weights   bool
	// Running is the loopback URL of a server already answering, or "".
	Running string
	Device  string
}

// Label is the one-line, harness-composed status for a picker row.
func (s Status) Label() string {
	switch {
	case s.Running != "":
		l := "running at " + strings.TrimPrefix(s.Running, "http://")
		if s.Device != "" {
			l += " on " + s.Device
		}
		return l
	case s.Installed && s.Weights:
		return "installed, starts on first use"
	case s.Installed:
		return "installed, weights not downloaded"
	}
	return "not installed: " + InstallHint
}

// Detect gathers Status without launching or downloading anything.
func Detect(ctx context.Context, m decisions.DeciderModel, o Options) Status {
	var st Status
	_, st.Installed = Binary()
	if rootDir, err := root(o); err == nil {
		st.Weights = Present(rootDir, m)
	}
	if base, h, ok := Running(ctx, o); ok {
		st.Running, st.Device = base, h.Device
	}
	return st
}

// HubProviders asks Hugging Face which of its inference providers serve m's
// repository. The answer is identifiers only; an error or an empty list both
// mean none. The token, when set, is sent to Hugging Face only.
func HubProviders(ctx context.Context, client *http.Client, m decisions.DeciderModel, token string) ([]string, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, localinfer.HFBase+"/api/models/"+m.Repo+"?expand[]=inferenceProviderMapping", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Hugging Face answered HTTP %d", resp.StatusCode)
	}
	return ParseHubProviders(io.LimitReader(resp.Body, 1<<20))
}

// ParseHubProviders reads the inferenceProviderMapping of a Hub model record
// and returns the provider names, reduced to identifiers and sorted.
func ParseHubProviders(r io.Reader) ([]string, error) {
	var body struct {
		Mapping json.RawMessage `json:"inferenceProviderMapping"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, err
	}
	var names []string
	var asMap map[string]json.RawMessage
	var asList []struct {
		Provider string `json:"provider"`
	}
	switch {
	case len(body.Mapping) == 0:
	case json.Unmarshal(body.Mapping, &asMap) == nil:
		for k := range asMap {
			names = append(names, k)
		}
	case json.Unmarshal(body.Mapping, &asList) == nil:
		for _, e := range asList {
			names = append(names, e.Provider)
		}
	}
	out := names[:0]
	for _, n := range names {
		if n = ident(n); n != "" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Supervisor keeps the process's handle and restarts a server that went
// away, at most once per decisionserver.RecoverEvery.
type Supervisor struct {
	Model decisions.DeciderModel
	Opts  Options

	mu     sync.Mutex
	handle *Handle
	last   time.Time
	busy   bool
}

// Adopt records a handle this process obtained elsewhere (the /model test).
func (s *Supervisor) Adopt(h *Handle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle != nil && s.handle != h {
		s.handle.Stop()
	}
	s.handle = h
}

// Handle returns the current handle, or nil.
func (s *Supervisor) Handle() *Handle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handle
}

// URL is the base URL decision calls use: the handle's, else the expected
// one.
func (s *Supervisor) URL() string {
	if h := s.Handle(); h != nil {
		return h.BaseURL
	}
	return BaseURL()
}

// Recover finds or relaunches the server in the background. Calls inside
// RecoverEvery of the last attempt, or while one runs, do nothing; meanwhile
// decision calls fail as unavailable and their fallback answers.
func (s *Supervisor) Recover() {
	s.mu.Lock()
	if s.busy || time.Since(s.last) < decisionserver.RecoverEvery {
		s.mu.Unlock()
		return
	}
	s.busy, s.last = true, time.Now()
	s.mu.Unlock()
	go func() {
		h, err := Ensure(context.Background(), s.Model, s.Opts)
		s.mu.Lock()
		s.busy = false
		if err == nil {
			if s.handle != nil && s.handle != h {
				s.handle.Stop()
			}
			s.handle = h
		}
		s.mu.Unlock()
	}()
}

// Stop stops the server if this process owns it.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	h := s.handle
	s.handle = nil
	s.mu.Unlock()
	h.Stop()
}

// Describe is a one-line, harness-composed description of a launch error
// for notices: never the server's log text.
func Describe(err error) string {
	switch {
	case errors.Is(err, ErrWeightsMissing):
		return "Strands Decider-2B is not downloaded; open /model and select it to download"
	case errors.Is(err, ErrNoBinary):
		return "strands-decider is not installed (" + InstallHint + "); decisions use the agent model"
	}
	var le *localinfer.LaunchError
	if errors.As(err, &le) {
		if le.Class == localinfer.LogOOM {
			return "Strands Decider-2B does not fit in memory"
		}
		return "the strands-decider server did not start; retest in /model"
	}
	return "Strands Decider-2B unavailable"
}

var (
	sharedMu sync.Mutex
	shared   = map[string]*Supervisor{}
)

// Shared returns this process's supervisor for m.
func Shared(m decisions.DeciderModel) *Supervisor {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if s, ok := shared[m.ID]; ok {
		return s
	}
	s := &Supervisor{Model: m}
	shared[m.ID] = s
	return s
}

// StopAll stops every server this process launched.
func StopAll() {
	sharedMu.Lock()
	list := make([]*Supervisor, 0, len(shared))
	for _, s := range shared {
		list = append(list, s)
	}
	sharedMu.Unlock()
	for _, s := range list {
		s.Stop()
	}
}
