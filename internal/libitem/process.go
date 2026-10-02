package libitem

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Process limits, shared with the website and the server.
const (
	MaxProcessCommand = 1024
	MaxProcessArgs    = 64
	MaxProcessArg     = 1024
	MaxProcessOptions = 64
	MaxProcessOptName = 128
	MaxProcessEnv     = 64
	MaxProcessEnvName = 128
	MaxProcessEnvVal  = 4096
	MaxProcessPath    = 1024
	MaxProcessUser    = 32
	// MaxProcessOrder is the highest process order; 0 means "no position of its own".
	MaxProcessOrder = 999
)

// Redirect modes.
const (
	RedirectLog     = "log"     // the process log (the default for stdout)
	RedirectDiscard = "discard" // the null device
	RedirectFile    = "file"    // a file, truncated at start
	RedirectAppend  = "append"  // a file, appended to
	RedirectStdout  = "stdout"  // stderr only: merged into stdout (the default for stderr)
)

// ProcessDoc is the structured form of a supervised process. Nothing here is a
// shell string: the command and its arguments are an argv, so no quoting rule
// applies and nothing is expanded.
type ProcessDoc struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Options []ProcessOption   `json:"options,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	User    string            `json:"user,omitempty"`
	Stdout  *Redirect         `json:"stdout,omitempty"`
	Stderr  *Redirect         `json:"stderr,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
	Order   *int              `json:"order,omitempty"`
}

// ProcessOption is one command-line option: its name, and its value as a
// separate argv element when it has one.
type ProcessOption struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

// Redirect says where one output stream goes.
type Redirect struct {
	Mode string  `json:"mode"`
	Path *string `json:"path,omitempty"`
}

var (
	processFields = []string{"name", "command", "args", "options", "env", "cwd", "user", "stdout", "stderr", "enabled", "order"}
	redirectModes = map[string]bool{"log": true, "discard": true, "file": true, "append": true, "stdout": true}
	optionNameRE  = regexp.MustCompile(`^-{1,2}[A-Za-z0-9][A-Za-z0-9._-]*$`)
	envNameRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	userRE        = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	secretNameRE  = regexp.MustCompile(`(?i)(secret|token|password|passwd|api[_-]?key|credential|private)`)
)

// EnvRefPrefix starts an env value that copies a variable of the host.
const EnvRefPrefix = "env:"

func init() { register(Process, validateProcess, nil) }

// EnvRef reads an env value of the form env:OTHER. ref is true when the value
// starts with env:, and ok when the rest is a legal variable name.
func EnvRef(value string) (other string, ref, ok bool) {
	rest, ref := strings.CutPrefix(value, EnvRefPrefix)
	if !ref {
		return "", false, false
	}
	return rest, true, len(rest) <= MaxProcessEnvName && envNameRE.MatchString(rest)
}

// LooksSecret reports whether an environment variable name reads as a secret.
func LooksSecret(name string) bool { return secretNameRE.MatchString(name) }

// ParseProcess validates a canonical structured-process document and decodes it.
func ParseProcess(canonical []byte) (ProcessDoc, error) {
	if _, err := validateProcess(canonical); err != nil {
		return ProcessDoc{}, err
	}
	var d ProcessDoc
	if err := json.Unmarshal(canonical, &d); err != nil {
		return ProcessDoc{}, refuse("the document does not match the schema")
	}
	return d, nil
}

// validateProcess checks a canonical process document and returns its name.
func validateProcess(canonical []byte) (string, error) {
	m, err := object(canonical)
	if err != nil {
		return "", err
	}
	if err := onlyKeys(m, "process", processFields...); err != nil {
		return "", err
	}
	name, err := docName(m, func(n string) bool { return ValidName(Process, n) }, nameRule)
	if err != nil {
		return "", err
	}
	if _, err := reqStr(m, "command", "process", MaxProcessCommand); err != nil {
		return "", err
	}
	args, _, err := list(m, "args", "process", MaxProcessArgs)
	if err != nil {
		return "", err
	}
	for i, a := range args {
		s, ok := a.(string)
		if !ok {
			return "", refuse("process.args[%d] must be a string", i)
		}
		if err := checkStr(s, "args["+itoa(i)+"]", "process", MaxProcessArg, true); err != nil {
			return "", err
		}
	}
	if err := validateOptions(m); err != nil {
		return "", err
	}
	if err := validateEnv(m); err != nil {
		return "", err
	}
	if _, err := optStr(m, "cwd", "process", MaxProcessPath, false); err != nil {
		return "", err
	}
	user, err := optStr(m, "user", "process", MaxProcessUser, false)
	if err != nil {
		return "", err
	}
	if user != "" && !userRE.MatchString(user) {
		return "", refuse("process.user %q is not a user name (lowercase letters, digits, _ and -, starting with a letter or _)", cleanForMessage(user))
	}
	for _, key := range []string{"stdout", "stderr"} {
		if err := validateRedirect(m, key); err != nil {
			return "", err
		}
	}
	if _, err := boolean(m, "enabled", "process", true); err != nil {
		return "", err
	}
	if _, _, err := whole(m, "order", "process", 0, MaxProcessOrder, 0); err != nil {
		return "", err
	}
	return name, nil
}

func validateOptions(m map[string]any) error {
	opts, _, err := list(m, "options", "process", MaxProcessOptions)
	if err != nil {
		return err
	}
	for i, v := range opts {
		o, err := entry(v, "process.options", i)
		if err != nil {
			return err
		}
		where := "process.options[" + itoa(i) + "]"
		if err := onlyKeys(o, where, "name", "value"); err != nil {
			return err
		}
		name, present, err := str(o, "name", where)
		if err != nil {
			return err
		}
		if !present || name == "" {
			return refuse("%s.name is required", where)
		}
		if len(name) > MaxProcessOptName || !optionNameRE.MatchString(name) {
			return refuse("%s.name %q is not an option name like -v or --verbose", where, cleanForMessage(name))
		}
		if _, err := optStr(o, "value", where, MaxProcessArg, true); err != nil {
			return err
		}
	}
	return nil
}

func validateEnv(m map[string]any) error {
	env, _, err := obj(m, "env", "process")
	if err != nil {
		return err
	}
	if len(env) > MaxProcessEnv {
		return refuse("process.env has %d variables; the most is %d", len(env), MaxProcessEnv)
	}
	// Sorted, so the first reason is the same on every call.
	for _, name := range sortedKeys(env) {
		if len(name) > MaxProcessEnvName || !envNameRE.MatchString(name) {
			return refuse("process.env name %q is not a variable name", cleanForMessage(name))
		}
		val, ok := env[name].(string)
		if !ok {
			return refuse("process.env.%s must be a string", name)
		}
		if err := checkStr(val, "env."+name, "process", MaxProcessEnvVal, true); err != nil {
			return err
		}
		if ref, isRef := strings.CutPrefix(val, EnvRefPrefix); isRef {
			if len(ref) > MaxProcessEnvName || !envNameRE.MatchString(ref) {
				return refuse("process.env.%s: %q is not env: followed by a variable name", name, cleanForMessage(val))
			}
			continue
		}
		if secretNameRE.MatchString(name) {
			return refuse("process.env.%s looks like a secret; use %sOTHER_VARIABLE so the value stays on the host", name, EnvRefPrefix)
		}
	}
	return nil
}

// validateRedirect checks stdout or stderr: {"mode", "path"?}. Mode stdout
// (merge into standard output) is only valid for stderr; a path is required for
// file and append and forbidden for every other mode.
func validateRedirect(m map[string]any, key string) error {
	r, present, err := obj(m, key, "process")
	if err != nil || !present {
		return err
	}
	where := "process." + key
	if err := onlyKeys(r, where, "mode", "path"); err != nil {
		return err
	}
	mode, ok, err := str(r, "mode", where)
	if err != nil {
		return err
	}
	if !ok || !redirectModes[mode] {
		return refuse("%s.mode must be log, discard, file, append or stdout", where)
	}
	if mode == "stdout" && key != "stderr" {
		return refuse("%s.mode stdout merges into standard output and is only valid for stderr", where)
	}
	path, hasPath, err := str(r, "path", where)
	if err != nil {
		return err
	}
	needs := mode == "file" || mode == "append"
	switch {
	case needs && (!hasPath || path == ""):
		return refuse("%s.path is required for mode %s", where, mode)
	case !needs && hasPath:
		return refuse("%s.path is only allowed for mode file or append", where)
	}
	if hasPath {
		return checkStr(path, "path", where, MaxProcessPath, false)
	}
	return nil
}

// Argv renders the command line: the command, then each option's name followed
// by its value as its own element when it has one, then args.
func (d ProcessDoc) Argv() []string {
	argv := []string{d.Command}
	for _, o := range d.Options {
		argv = append(argv, o.Name)
		if o.Value != nil {
			argv = append(argv, *o.Value)
		}
	}
	return append(argv, d.Args...)
}

// IsEnabled is the document's enabled flag, true when it says nothing.
func (d ProcessDoc) IsEnabled() bool { return d.Enabled == nil || *d.Enabled }

// OrderOr is the document's order, or def when it says none or says 0.
func (d ProcessDoc) OrderOr(def int) int {
	if d.Order == nil || *d.Order == 0 {
		return def
	}
	return *d.Order
}

// LegacyProcess is the document a legacy NNN-slug.sh file syncs as: the whole
// file is one sh -c string. A body longer than one argument may be cannot travel.
func LegacyProcess(name, body string, order int, enabled bool) (ProcessDoc, error) {
	d := ProcessDoc{Name: name, Command: "sh", Args: []string{"-c", body}}
	if order > 0 {
		d.Order = &order
	}
	if !enabled {
		f := false
		d.Enabled = &f
	}
	b, err := json.Marshal(d)
	if err != nil {
		return ProcessDoc{}, err
	}
	canonical, err := CanonicalJSON(b)
	if err != nil {
		return ProcessDoc{}, err
	}
	if _, err := validateProcess(canonical); err != nil {
		return ProcessDoc{}, err
	}
	return d, nil
}

// Display renders the command line for people: the argv, each word quoted only if
// it needs it. It is for the screen and the log; nothing runs it.
func (d ProcessDoc) Display() string {
	words := d.Argv()
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = quoteWord(w)
	}
	return strings.Join(out, " ")
}

func quoteWord(w string) string {
	if w == "" {
		return "''"
	}
	plain := true
	for _, r := range w {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			strings.ContainsRune("._/@%+=:,-", r):
		default:
			plain = false
		}
	}
	if plain {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// NormalizeProcessFile reads a process file kept on the host and returns the
// item the library would hold for it. The file name is authoritative for what it
// can say: the slug is the name, a disabled file is `enabled: false`, and an
// `order` that disagrees with the file name's number is the file name's. A key the
// file leaves out stays out, so a document installed from the library, which
// the host wrote under its own order and state, exports as the same bytes and the
// same hash. A file may leave out its name (the file name gives it).
func NormalizeProcessFile(raw []byte, slug string, order int, enabled bool) (Item, error) {
	o, err := decodeObject(raw)
	if err != nil {
		return Item{}, err
	}
	o["name"] = slug
	if !enabled {
		o["enabled"] = false
	} else if v, ok := o["enabled"]; ok && v == false {
		delete(o, "enabled")
	}
	if v, ok := o["order"]; ok {
		if n, isNum := v.(json.Number); isNum {
			if i, err := n.Int64(); err == nil && i != 0 && int(i) != order {
				o["order"] = order
			}
		}
	}
	b, err := encodeCanonical(o)
	if err != nil {
		return Item{}, err
	}
	return Validate(Process, b)
}
