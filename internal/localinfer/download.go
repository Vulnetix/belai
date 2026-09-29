package localinfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/proc"
)

// hfHost is the only host the Hugging Face token is ever sent to. Downloads
// redirect to a CDN; Go drops the Authorization header on a cross-host
// redirect, and fetchJSON refuses redirects outright.
const hfHost = "huggingface.co"

// HFBase is the Hugging Face origin. Tests point it at an httptest server.
var HFBase = "https://" + hfHost

// ModelsDir is where belai keeps model files it downloaded itself, outside
// the Hugging Face cache: <user cache>/belai/models. The CLI path, when a
// genuine Hugging Face CLI is present, uses the hub cache instead.
func ModelsDir() (string, error) {
	if v := os.Getenv("BELAI_MODELS_DIR"); v != "" {
		return v, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "belai", "models"), nil
}

// ownPath is where DownloadFile stores repo/file.
func ownPath(repo, file string) (string, error) {
	dir, err := ModelsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, strings.ReplaceAll(repo, "/", "--"), filepath.Base(file)), nil
}

// FindModelFile looks for exactly repo/file on disk: belai's own models
// directory, the Hugging Face hub cache (snapshots), and llama.cpp's -hf cache.
// It returns "" when the file is not present in full. A partial download
// (".part", ".incomplete") never counts.
func FindModelFile(repo, file string) string {
	if p, err := ownPath(repo, file); err == nil {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return p
		}
	}
	base := filepath.Base(file)
	for _, hub := range hubDirs() {
		snap := filepath.Join(hub, "models--"+strings.ReplaceAll(repo, "/", "--"), "snapshots")
		revs, err := os.ReadDir(snap)
		if err != nil {
			continue
		}
		for _, rev := range revs {
			p := filepath.Join(snap, rev.Name(), file)
			if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
				return p
			}
		}
	}
	for _, dir := range llamaCacheDirs() {
		// llama.cpp names -hf downloads "<org>_<repo>_<file>".
		p := filepath.Join(dir, strings.ReplaceAll(repo, "/", "_")+"_"+base)
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			return p
		}
	}
	return ""
}

func hubDirs() []string {
	var out []string
	if v := os.Getenv("HF_HUB_CACHE"); v != "" {
		out = append(out, v)
	}
	if v := os.Getenv("HF_HOME"); v != "" {
		out = append(out, filepath.Join(v, "hub"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".cache", "huggingface", "hub"))
	}
	return out
}

func llamaCacheDirs() []string {
	var out []string
	if v := os.Getenv("LLAMA_CACHE"); v != "" {
		out = append(out, v)
	}
	if dir, err := os.UserCacheDir(); err == nil {
		out = append(out, filepath.Join(dir, "llama.cpp"))
	}
	return out
}

// RemoteFile is what the Hugging Face API says about one file of a repo.
type RemoteFile struct {
	Size   int64
	SHA256 string // lower-case hex; empty when the file is not LFS-tracked
}

// RemoteInfo asks the Hugging Face API for a file's size and SHA-256. The
// token, when set, is sent only to HFBase; redirects are refused.
func RemoteInfo(ctx context.Context, client *http.Client, repo, file, token string) (RemoteFile, error) {
	u := HFBase + "/api/models/" + repo + "?blobs=true"
	var body struct {
		Siblings []struct {
			Rfilename string `json:"rfilename"`
			Size      int64  `json:"size"`
			LFS       *struct {
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if err := fetchJSON(ctx, client, u, token, &body); err != nil {
		return RemoteFile{}, err
	}
	for _, s := range body.Siblings {
		if s.Rfilename != file {
			continue
		}
		rf := RemoteFile{Size: s.Size}
		if s.LFS != nil {
			rf.SHA256 = strings.ToLower(s.LFS.SHA256)
			if s.LFS.Size > 0 {
				rf.Size = s.LFS.Size
			}
		}
		return rf, nil
	}
	return RemoteFile{}, &DownloadError{Class: DownloadNotFound, Msg: fmt.Sprintf("%s has no file %s", repo, file)}
}

func fetchJSON(ctx context.Context, client *http.Client, u, token string, out any) error {
	if client == nil {
		client = http.DefaultClient
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		return &DownloadError{Class: DownloadNetwork, Msg: "reach Hugging Face", Err: err}
	}
	defer resp.Body.Close()
	if err := statusError(resp.StatusCode); err != nil {
		return err
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// DownloadClass names why a download failed.
type DownloadClass string

const (
	DownloadNetwork  DownloadClass = "network"
	DownloadAuth     DownloadClass = "auth"
	DownloadNotFound DownloadClass = "not-found"
	DownloadDisk     DownloadClass = "disk"
	DownloadChecksum DownloadClass = "checksum"
	DownloadRate     DownloadClass = "rate"
)

// DownloadError is a download failure with its class.
type DownloadError struct {
	Class DownloadClass
	Msg   string
	Err   error
}

func (e *DownloadError) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func (e *DownloadError) Unwrap() error { return e.Err }

func statusError(code int) error {
	switch {
	case code == http.StatusOK || code == http.StatusPartialContent:
		return nil
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return &DownloadError{Class: DownloadAuth, Msg: fmt.Sprintf("Hugging Face refused access (HTTP %d)", code)}
	case code == http.StatusNotFound:
		return &DownloadError{Class: DownloadNotFound, Msg: "Hugging Face has no such repo or file (HTTP 404)"}
	case code == http.StatusTooManyRequests:
		return &DownloadError{Class: DownloadRate, Msg: "Hugging Face rate-limited the request (HTTP 429)"}
	default:
		return &DownloadError{Class: DownloadNetwork, Msg: fmt.Sprintf("Hugging Face answered HTTP %d", code)}
	}
}

// Progress reports bytes written so far out of total (total may be 0 when
// unknown).
type Progress func(done, total int64)

// DownloadFile fetches repo/file from Hugging Face into ModelsDir over HTTPS,
// resuming a partial ".part" file with a Range request, and verifies the
// SHA-256 the API reports before moving it into place. It needs no CLI.
// The token, when set, rides only on requests to HFBase.
func DownloadFile(ctx context.Context, client *http.Client, repo, file, token string, want RemoteFile, progress Progress) (string, error) {
	dest, err := ownPath(repo, file)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", &DownloadError{Class: DownloadDisk, Msg: "create models directory", Err: err}
	}
	part := dest + ".part"
	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
		if want.Size > 0 && have > want.Size {
			_ = os.Remove(part)
			have = 0
		}
	}

	if client == nil {
		client = http.DefaultClient
	}
	u := HFBase + "/" + repo + "/resolve/main/" + escapePath(file)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", &DownloadError{Class: DownloadNetwork, Msg: "download " + file, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && want.Size > 0 && have == want.Size {
		// The part file is already complete; fall through to verification.
	} else {
		if err := statusError(resp.StatusCode); err != nil {
			return "", err
		}
		flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if resp.StatusCode == http.StatusOK {
			// The server ignored the Range: start over.
			flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
			have = 0
		}
		f, err := os.OpenFile(part, flags, 0o644)
		if err != nil {
			return "", &DownloadError{Class: DownloadDisk, Msg: "open " + part, Err: err}
		}
		total := want.Size
		if total == 0 && resp.ContentLength > 0 {
			total = have + resp.ContentLength
		}
		w := &progressWriter{w: f, done: have, total: total, report: progress}
		_, cerr := io.Copy(w, resp.Body)
		w.flush()
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		if cerr != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if isDiskFull(cerr) {
				return "", &DownloadError{Class: DownloadDisk, Msg: "write " + part, Err: cerr}
			}
			return "", &DownloadError{Class: DownloadNetwork, Msg: "download interrupted (it resumes on retry)", Err: cerr}
		}
	}

	if want.Size > 0 {
		if fi, err := os.Stat(part); err != nil || fi.Size() != want.Size {
			return "", &DownloadError{Class: DownloadNetwork, Msg: "download incomplete (it resumes on retry)"}
		}
	}
	if want.SHA256 != "" {
		sum, err := fileSHA256(ctx, part)
		if err != nil {
			return "", err
		}
		if sum != want.SHA256 {
			_ = os.Remove(part)
			return "", &DownloadError{Class: DownloadChecksum, Msg: "downloaded file failed its SHA-256 check and was removed"}
		}
	}
	if err := os.Rename(part, dest); err != nil {
		return "", &DownloadError{Class: DownloadDisk, Msg: "move download into place", Err: err}
	}
	return dest, nil
}

// RemoveModelFile deletes belai's own copy of repo/file (and any partial
// download), so a corrupt file can be fetched again. Files in the Hugging
// Face or llama.cpp caches are left alone; they belong to those tools.
func RemoveModelFile(repo, file string) error {
	p, err := ownPath(repo, file)
	if err != nil {
		return err
	}
	_ = os.Remove(p + ".part")
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func escapePath(file string) string {
	parts := strings.Split(file, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func fileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isDiskFull(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no space left") || strings.Contains(s, "disk quota")
}

// progressWriter reports progress at most every 200ms.
type progressWriter struct {
	w           io.Writer
	done, total int64
	report      Progress
	last        time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.done += int64(n)
	if p.report != nil && time.Since(p.last) > 200*time.Millisecond {
		p.last = time.Now()
		p.report(p.done, p.total)
	}
	return n, err
}

func (p *progressWriter) flush() {
	if p.report != nil {
		p.report(p.done, p.total)
	}
}

var (
	hfCLIOnce sync.Once
	hfCLIPath string
)

// HFCLI returns a Hugging Face CLI binary, checked to really be one: another
// program named "hf" (there is at least one hidden-file utility) would
// otherwise be handed download arguments. The result is memoised.
func HFCLI() (string, bool) {
	hfCLIOnce.Do(func() { hfCLIPath, _ = HFBinary() })
	return hfCLIPath, hfCLIPath != ""
}

// HFDownloadFile asks the Hugging Face CLI for exactly one file of a repo and
// returns its local path in the hub cache. The CLI runs with the scrubbed
// environment plus HF_TOKEN (never argv), in its own process group.
func HFDownloadFile(ctx context.Context, binary, repo, file, token string, sink func(string)) (string, error) {
	if binary == "" {
		return "", fmt.Errorf("hf binary is required")
	}
	cmd := exec.CommandContext(ctx, binary, "download", repo, file)
	proc.SetProcessGroup(cmd)
	cmd.Env = proc.ScrubbedEnv()
	if token != "" {
		cmd.Env = append(cmd.Env, "HF_TOKEN="+token)
	}
	tee := proc.NewLineTee(0, sink)
	cmd.Stdout = tee
	cmd.Stderr = tee
	if err := cmd.Run(); err != nil {
		tee.Flush()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &DownloadError{Class: DownloadNetwork, Msg: "hf download failed", Err: errors.New(lastLine(tee.Content()))}
	}
	tee.Flush()
	if p := parseGGUFPath(tee.Content()); p != "" {
		return p, nil
	}
	if p := FindModelFile(repo, file); p != "" {
		return p, nil
	}
	return "", &DownloadError{Class: DownloadNotFound, Msg: "hf download finished but the file was not found in the cache"}
}

// HubPartialSize is the size of the newest partial download the Hugging Face
// CLI is writing for repo, used to report progress while the CLI runs.
func HubPartialSize(repo string) int64 {
	var best int64
	var newest time.Time
	for _, hub := range hubDirs() {
		blobs := filepath.Join(hub, "models--"+strings.ReplaceAll(repo, "/", "--"), "blobs")
		entries, err := os.ReadDir(blobs)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".incomplete") {
				continue
			}
			if fi, err := e.Info(); err == nil && fi.ModTime().After(newest) {
				newest, best = fi.ModTime(), fi.Size()
			}
		}
	}
	return best
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// RepoFile is one file of a Hugging Face repo.
type RepoFile struct {
	Name string
	RemoteFile
}

// RepoGGUFs lists a repo's GGUF files, leaving out vision projectors
// (mmproj), which are not models on their own.
func RepoGGUFs(ctx context.Context, client *http.Client, repo, token string) ([]RepoFile, error) {
	u := HFBase + "/api/models/" + repo + "?blobs=true"
	var body struct {
		Siblings []struct {
			Rfilename string `json:"rfilename"`
			Size      int64  `json:"size"`
			LFS       *struct {
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if err := fetchJSON(ctx, client, u, token, &body); err != nil {
		return nil, err
	}
	var out []RepoFile
	for _, s := range body.Siblings {
		lower := strings.ToLower(s.Rfilename)
		if !strings.HasSuffix(lower, ".gguf") || strings.Contains(lower, "mmproj") {
			continue
		}
		rf := RepoFile{Name: s.Rfilename, RemoteFile: RemoteFile{Size: s.Size}}
		if s.LFS != nil {
			rf.SHA256 = strings.ToLower(s.LFS.SHA256)
			if s.LFS.Size > 0 {
				rf.Size = s.LFS.Size
			}
		}
		out = append(out, rf)
	}
	return out, nil
}

// PickGGUF chooses the file for quant (case-insensitive) from files, the
// smallest when several match (a split model's first shard sorts first). It
// returns false when none matches.
func PickGGUF(files []RepoFile, quant string) (RepoFile, bool) {
	var best RepoFile
	found := false
	q := strings.ToLower(quant)
	for _, f := range files {
		if !strings.Contains(strings.ToLower(filepath.Base(f.Name)), q) {
			continue
		}
		if !found || f.Size < best.Size {
			best, found = f, true
		}
	}
	return best, found
}
