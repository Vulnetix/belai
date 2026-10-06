package bgproc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/proc"
)

// A structured process (libitem.ProcessDoc) runs as an argv: command, then each
// option's name and value, then args. No shell is involved, so nothing is quoted,
// split or expanded, and a value with a space or a semicolon is one argument.
//
// The environment is the scrubbed one (proc.ScrubbedEnv) plus the document's env.
// A value of the form env:OTHER copies the host variable OTHER at start, from the
// unscrubbed environment: that is how a process gets a secret the library never
// holds. A missing variable refuses the start, naming it.
//
// `user` is honoured only when Belai runs as root. Anywhere else the process is
// refused: it never quietly runs as the user who runs Belai.
//
// stdout and stderr each go to the process log, nowhere, or a file. stderr may be
// merged into stdout. A path is `~`, absolute, or relative to the project
// directory; a file is created 0600 (replaced at each start for `file`, appended
// to for `append`), never through a symbolic link, and its directory must exist.

// specRun is what one start of a structured process opened: the redirect files,
// the credential to run as, and what to do with them once started.
type specRun struct {
	doc     libitem.ProcessDoc
	stdout  io.Writer // nil means the null device
	stderr  io.Writer
	merge   bool // stderr follows stdout
	closers []io.Closer
	// user is the account the process runs as; empty means the current one.
	user string
}

func (r *specRun) close() {
	for _, c := range r.closers {
		_ = c.Close()
	}
	r.closers = nil
}

// wire connects the command's output. logw is the process log and live tail.
func (r *specRun) wire(ec *exec.Cmd, logw io.Writer) {
	pick := func(w io.Writer) io.Writer {
		if w == logSentinel {
			return logw
		}
		return w
	}
	ec.Stdout = pick(r.stdout)
	if r.merge {
		ec.Stderr = ec.Stdout
		return
	}
	ec.Stderr = pick(r.stderr)
}

// logSentinel stands for the process log until the writer is known.
var logSentinel io.Writer = sentinel{}

type sentinel struct{}

func (sentinel) Write(p []byte) (int, error) { return len(p), nil }

// validateSpec rechecks a document at the point it runs: the library validated
// it, but a file edited by hand is only as good as this check.
func validateSpec(doc libitem.ProcessDoc) error {
	b, err := jsonOf(doc)
	if err != nil {
		return err
	}
	if _, err := libitem.Validate(libitem.Process, b); err != nil {
		return fmt.Errorf("the process is not valid: %w", err)
	}
	return nil
}

// specCommand builds the command for a structured process, opening what it needs.
// Nothing stays open when it fails.
func (m *Manager) specCommand(p *processInstance) (*exec.Cmd, *specRun, error) {
	doc := *p.spec
	argv := doc.Argv()
	ec := exec.CommandContext(p.ctx, argv[0], argv[1:]...)
	run := &specRun{doc: doc, user: doc.User}
	fail := func(err error) (*exec.Cmd, *specRun, error) {
		run.close()
		return nil, nil, err
	}
	// A user that cannot be honoured refuses the start before a file is opened.
	if err := run.checkUser(); err != nil {
		return fail(err)
	}

	home, _ := os.UserHomeDir()
	cwd, err := resolveSpecPath(doc.Cwd, m.workdir, home)
	if err != nil {
		return fail(fmt.Errorf("working directory: %w", err))
	}
	if cwd == "" {
		cwd = m.workdir
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return fail(fmt.Errorf("working directory %s does not exist", cwd))
	}
	ec.Dir = cwd

	env, err := m.specEnv(doc)
	if err != nil {
		return fail(err)
	}
	ec.Env = env

	if run.stdout, run.stderr, run.merge, err = m.openRedirects(run, doc, m.workdir, home); err != nil {
		return fail(err)
	}
	return ec, run, nil
}

// specEnv is the process environment: the scrubbed one, then the document's env
// in name order, each env:OTHER read from the host.
func (m *Manager) specEnv(doc libitem.ProcessDoc) ([]string, error) {
	lookup := m.lookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	set := map[string]string{}
	order := []string{}
	add := func(k, v string) {
		if _, ok := set[k]; !ok {
			order = append(order, k)
		}
		set[k] = v
	}
	for _, kv := range proc.ScrubbedEnv() {
		k, v, _ := strings.Cut(kv, "=")
		add(k, v)
	}
	names := make([]string, 0, len(doc.Env))
	for k := range doc.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := doc.Env[k]
		if other, ref, _ := libitem.EnvRef(v); ref {
			host, ok := lookup(other)
			if !ok {
				return nil, fmt.Errorf("environment variable %s is not set on this host (env %s copies it)", other, k)
			}
			v = host
		}
		add(k, v)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+set[k])
	}
	return out, nil
}

// openRedirects opens the files stdout and stderr name. Both naming one path
// share one file.
func (m *Manager) openRedirects(run *specRun, doc libitem.ProcessDoc, projectDir, home string) (stdout, stderr io.Writer, merge bool, err error) {
	files := map[string]*os.File{}
	open := func(r *libitem.Redirect, which string) (io.Writer, error) {
		if r == nil {
			return nil, nil
		}
		switch r.Mode {
		case libitem.RedirectLog, "":
			return logSentinel, nil
		case libitem.RedirectDiscard:
			return nil, nil
		case libitem.RedirectStdout:
			return nil, nil // handled by the caller
		}
		path, err := resolveSpecPath(*r.Path, projectDir, home)
		if err != nil {
			return nil, fmt.Errorf("%s path: %w", which, err)
		}
		if f, ok := files[path]; ok {
			return f, nil
		}
		f, err := openRedirectFile(path, r.Mode == libitem.RedirectAppend)
		if err != nil {
			return nil, fmt.Errorf("%s file: %w", which, err)
		}
		if run.user != "" {
			if err := chownToUser(f, run.user); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("%s file: %w", which, err)
			}
		}
		files[path] = f
		run.closers = append(run.closers, f)
		return f, nil
	}
	// stdout defaults to the log.
	so := doc.Stdout
	if so == nil {
		so = &libitem.Redirect{Mode: libitem.RedirectLog}
	}
	if stdout, err = open(so, "stdout"); err != nil {
		return nil, nil, false, err
	}
	// stderr defaults to merging into stdout.
	se := doc.Stderr
	if se == nil || se.Mode == libitem.RedirectStdout {
		return stdout, nil, true, nil
	}
	if stderr, err = open(se, "stderr"); err != nil {
		return nil, nil, false, err
	}
	return stdout, stderr, false, nil
}

// openRedirectFile opens a redirect target 0600, truncating or appending, and
// refuses a symbolic link or anything but a regular file.
func openRedirectFile(path string, appendTo bool) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s is a symbolic link", path)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	flags := os.O_WRONLY | os.O_CREATE
	if appendTo {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o600)
}

// resolveSpecPath turns a path a document names into an absolute one: `~` and
// `~/x` are in the home directory, an absolute path is cleaned, and a relative
// one is under the project directory and may not climb out of it. An empty path
// stays empty.
func resolveSpecPath(p, projectDir, home string) (string, error) {
	switch {
	case p == "":
		return "", nil
	case p == "~" || strings.HasPrefix(p, "~/"):
		if home == "" {
			return "", errors.New("no home directory to resolve ~ against")
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	case strings.HasPrefix(p, "~"):
		return "", fmt.Errorf("%q: only ~ and ~/ are supported", p)
	case filepath.IsAbs(p):
		return filepath.Clean(p), nil
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return "", fmt.Errorf("%q leaves the project directory", p)
		}
	}
	return filepath.Join(projectDir, p), nil
}

// jsonOf renders a document for Validate.
func jsonOf(doc libitem.ProcessDoc) ([]byte, error) { return json.Marshal(doc) }
