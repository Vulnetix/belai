package vulnetixcli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/httpclient"
)

// Capabilities is everything Signet knows about the local Vulnetix CLI.
type Capabilities struct {
	Present        bool
	Path, RealPath string
	Version        Version
	Commit         string
	BuildDate      time.Time
	Install        InstallMethod
	InstallPrefix  string
	Update         UpdateStatus
	Auth           AuthState
	API            APIState
	Env            EnvInfo
	Web            WebURLs
	Probed         time.Time
	Degraded       []string
}

// ProbeOptions customises CLI probing. A nil Client uses httpclient.Default().
type ProbeOptions struct {
	Client       *http.Client
	SkipNetwork  bool
	Now          func() time.Time
	Getenv       func(string) string
	ReleaseCache *ReleaseCache
	// Observer, when non-nil, receives an activity registration per probe
	// subprocess so /code-review configure and status can stream their probes.
	Observer RunObserver
}

// RunObserver is the optional activity-register seam for CLI executions. The
// TUI injects it; the interface is structural, so the same concrete observer
// satisfies commands.RunObserver too.
type RunObserver interface {
	Start(name string, argv []string, dir string, cancel context.CancelFunc) (sink func(string), done func(exitCode int, timedOut bool, err error))
}

// APIState describes reachability and credential validity independently.
type APIState struct {
	Reachable         bool
	ReachabilityError string
	CredentialsValid  bool
	CredentialError   string
	URL               string
}

// Probe collects capabilities from the vulnetix binary. It does as much as
// possible without network access, then optionally checks the GitHub release
// API and the configured Vulnetix API.
func Probe(ctx context.Context, c CLI, opts ProbeOptions) Capabilities {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.Client == nil {
		opts.Client = httpclient.Default()
	}

	cap := Capabilities{
		Path:   c.Path,
		Probed: opts.Now(),
	}

	real, err := filepath.EvalSymlinks(c.Path)
	if err == nil {
		realAbs, err := filepath.Abs(real)
		if err == nil {
			cap.RealPath = realAbs
		} else {
			cap.RealPath = real
		}
	}

	if c.Path == "" {
		cap.Degraded = append(cap.Degraded, "vulnetix binary not found")
		return cap
	}

	if _, err := os.Stat(c.Path); err != nil {
		cap.Degraded = append(cap.Degraded, fmt.Sprintf("binary not accessible: %v", err))
		return cap
	}
	cap.Present = true

	// Try multiple version sources, fastest first.
	cap.Version = probeVersionSources(ctx, &c, cap.RealPath, &cap.Degraded)

	// Install method from the resolved path.
	cap.Install, cap.InstallPrefix = DetectInstall(c.Path, opts.Getenv)

	var wg sync.WaitGroup
	var envText, authText string
	var envErr, versionErr, authErr error

	wg.Add(3)
	go func() {
		defer wg.Done()
		res, err := execObserved(ctx, c, opts.Observer, "vulnetix version", "", "--disable-memory", "version")
		if err == nil {
			versionErr = nil
		} else {
			versionErr = err
		}
		_ = res // version text already captured in probeVersionSources
	}()
	go func() {
		defer wg.Done()
		res, err := execObserved(ctx, c, opts.Observer, "vulnetix env", "", "--disable-memory", "env")
		if err == nil {
			envText = res.Stdout
		} else {
			envErr = err
		}
	}()
	go func() {
		defer wg.Done()
		res, err := execObserved(ctx, c, opts.Observer, "vulnetix auth status", "", "--disable-memory", "auth", "status")
		if err == nil {
			authText = res.Stdout
		} else {
			authErr = err
		}
	}()
	wg.Wait()

	if versionErr != nil {
		cap.Degraded = append(cap.Degraded, fmt.Sprintf("version probe failed: %v", versionErr))
	}
	if envErr != nil {
		cap.Degraded = append(cap.Degraded, fmt.Sprintf("env probe failed: %v", envErr))
	}
	if envText != "" {
		cap.Env = ParseEnv(envText)
	}
	if authErr != nil {
		cap.Degraded = append(cap.Degraded, fmt.Sprintf("auth probe failed: %v", authErr))
	}
	if authText != "" {
		cap.Auth = ParseAuthStatus(authText)
	}

	// Update status: always safe because the cache is local; network is optional.
	if cap.Version.IsZero() {
		cap.Update = UpdateStatus{Error: "could not determine current version"}
	} else if !opts.SkipNetwork {
		latest, url, err := LatestRelease(ctx, opts.Client, opts.Getenv, opts.ReleaseCache)
		if err != nil {
			cap.Update = UpdateStatus{Current: cap.Version, Error: err.Error()}
		} else {
			cap.Update = UpdateStatus{
				Available: latest.Compare(cap.Version) > 0,
				Latest:    latest,
				Current:   cap.Version,
				URL:       url,
			}
		}
	}

	// API reachability and web URLs.
	baseURL := opts.Getenv("VULNETIX_API_URL")
	if baseURL == "" {
		baseURL = "https://api.vdb.vulnetix.com/v1"
	}
	cap.API.URL = baseURL
	if !opts.SkipNetwork {
		cap.API.Reachable = checkReachability(ctx, opts.Client, baseURL)
		if cap.API.Reachable {
			cap.API.ReachabilityError = ""
		} else {
			cap.API.ReachabilityError = "API not reachable"
		}
		if cap.Auth.Authenticated {
			cap.API.CredentialsValid = checkCredentialValidity(ctx, c, &cap.API.CredentialError)
		}
	}

	web, err := ResolveURLs(opts.Getenv, cap.Auth.Authenticated)
	if err != nil {
		cap.Degraded = append(cap.Degraded, fmt.Sprintf("web URL resolution failed: %v", err))
	}
	cap.Web = web

	return cap
}

// probeVersionSources tries --version, version, env, and finally the path.
func probeVersionSources(ctx context.Context, c *CLI, realPath string, degraded *[]string) Version {
	// Fast --version.
	if res, err := c.ExecIn(ctx, "", "--disable-memory", "--version"); err == nil && res.Stdout != "" {
		if v, ok := ParseVersion(res.Stdout); ok {
			return v
		}
	}
	if res, err := c.ExecIn(ctx, "", "--disable-memory", "version"); err == nil {
		if v, ok := ParseVersion(res.Stdout); ok {
			return v
		}
	}
	if res, err := c.ExecIn(ctx, "", "--disable-memory", "env"); err == nil {
		secs := sections(stripANSI(res.Stdout))
		if block, ok := secs["VERSION"]; ok {
			if v, ok := ParseVersion(block); ok {
				return v
			}
		}
	}
	if realPath != "" {
		if v, ok := ParseVersion(realPath); ok {
			return v
		}
	}
	*degraded = append(*degraded, "could not parse installed version")
	return Version{}
}

// checkReachability performs a credential-free GET against the API base.
func checkReachability(ctx context.Context, client *http.Client, url string) bool {
	if client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// execObserved runs one CLI execution through the optional observer, streaming
// when one is present and falling back to ExecIn when not.
func execObserved(ctx context.Context, c CLI, obs RunObserver, name, dir string, args ...string) (Result, error) {
	if obs == nil {
		return c.ExecIn(ctx, dir, args...)
	}
	subCtx, cancel := context.WithCancel(ctx)
	sink, done := obs.Start(name, append([]string{c.Path}, HardenedArgs(args...)...), dir, cancel)
	res, err := c.ExecStreamIn(subCtx, dir, sink, args...)
	done(res.ExitCode, res.TimedOut, err)
	cancel()
	return res, err
}

// checkCredentialValidity runs `vulnetix auth verify` and returns whether the
// existing credentials are valid.
func checkCredentialValidity(ctx context.Context, c CLI, errOut *string) bool {
	res, err := c.ExecIn(ctx, "", "--disable-memory", "auth", "verify")
	if err != nil {
		*errOut = err.Error()
		return false
	}
	ok := markerOK.MatchString(res.Stdout + res.Stderr)
	verified := regexp.MustCompile(`(?i)\bverified\b`).MatchString(res.Stdout + res.Stderr)
	if !(ok || verified) {
		*errOut = "credentials not verified"
		return false
	}
	return true
}
