package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// ─────────────────────────────────────────────────────────────────────────
// The hooks dialect: the definition format Claude Code, Codex and the
// Vulnetix CLI's `agent install` share, read from a hook BUNDLE.
//
// A bundle is a directory under the global hooks directory holding hooks.json
// (the definition) and the script files its commands run:
//
//	{"name": "pix", "description": "…",
//	 "hooks": {"PreToolUse": [{"matcher": "Bash|Edit",
//	           "hooks": [{"type": "command", "command": "vulnetix agent hook", "timeout": 30}]}]}}
//
// Belai's own flat hook files (validate.go) keep working unchanged. A bundle
// differs in three ways, each held to the same invariants:
//
//   - The command is tokenised once, here, into a fixed argv (never a shell).
//     Its first word is a regular file inside the bundle (resolved after
//     symlinks, as ever) or a bare program name the user listed in
//     hooks.allowed_programs. Every other word must be a plain token that names
//     nothing outside the bundle.
//   - The stdin payload and the stdout/exit-code answer use the dialect's
//     protocol (see claude.go), mapped onto Belai's deny/ask/allow, and a
//     blocking hook that fails, times out or answers nonsense still denies.
//   - Events map one to one onto Belai's (PreToolUse is pre_tool, and so on).
//
// Only the user's own global directory is read, never a project's.
// ─────────────────────────────────────────────────────────────────────────

// DialectClaude marks a hook read from a bundle's definition.
const DialectClaude = "claude"

// BundleDefinitionFile is the definition a bundle holds. A bundle's own files
// may not use this name.
const BundleDefinitionFile = "hooks.json"

// Bundle bounds.
const (
	// MaxBundleDefinitionBytes caps hooks.json.
	MaxBundleDefinitionBytes = 64 << 10
	// MaxBundleHooks caps the commands one bundle defines.
	MaxBundleHooks = 64
	// DialectDefaultTimeoutMS is the timeout of a bundle hook that names none.
	DialectDefaultTimeoutMS = 30_000
	maxCommandBytes         = 512
	maxMatcherBytes         = 128
)

var (
	bundleNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// programNameRE is what hooks.allowed_programs may list: a bare program name.
	programNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	// tokenRE is every word of a bundle command: nothing a shell would treat
	// specially, and no `~`, `$`, quote, glob or redirect.
	tokenRE = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,\- ]+$`)
	// matcherAltRE is one alternative of a translated matcher.
	matcherAltRE = regexp.MustCompile(`^[A-Za-z0-9_*:-]+$`)
)

// ValidProgramName reports whether s can be listed in hooks.allowed_programs.
func ValidProgramName(s string) bool { return programNameRE.MatchString(s) }

// ValidBundleName reports whether s can name a bundle directory.
func ValidBundleName(s string) bool { return bundleNameRE.MatchString(s) }

// claudeEvents maps the dialect's event names onto Belai's. Nothing maps to
// pre_edit or post_edit: pre_tool and post_tool fire for edits too, and a
// matcher of Edit|Write narrows them.
var claudeEvents = map[string]string{
	"PreToolUse":       EventPreTool,
	"PostToolUse":      EventPostTool,
	"UserPromptSubmit": EventUserPromptSubmit,
	"SessionStart":     EventSessionStart,
	"SessionEnd":       EventSessionEnd,
	"Stop":             EventStop,
	"SubagentStop":     EventSubagentStop,
	"PreCompact":       EventPreCompact,
	"Notification":     EventNotification,
}

// claudeEventOrder is the order a bundle's events are named in, so a hook's
// name (and so the order it runs in) never depends on map iteration.
var claudeEventOrder = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact",
	"Stop", "SubagentStop", "Notification", "SessionEnd",
}

// ClaudeEventName is the dialect's name for a Belai event ("" when it has none).
func ClaudeEventName(event string) string {
	for name, ev := range claudeEvents {
		if ev == event {
			return name
		}
	}
	return ""
}

// Parsed is a bundle definition read and validated against a directory.
type Parsed struct {
	Name        string
	Description string
	Hooks       []*Hook
	// Notes say what was skipped (a handler type or event Belai does not run).
	Notes []string
}

type dialectHandler struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout"`
}

type dialectGroup struct {
	Matcher string           `json:"matcher"`
	Hooks   []dialectHandler `json:"hooks"`
}

// ParseDefinition reads one bundle definition and validates every command
// against dir (the bundle's directory) and allowed (hooks.allowed_programs).
// One invalid command rejects the whole definition: a bundle is installed as a
// unit and never half-loaded. bundle is the directory's name.
func ParseDefinition(bundle, dir string, data []byte, allowed []string) (*Parsed, error) {
	if len(data) > MaxBundleDefinitionBytes {
		return nil, fmt.Errorf("the definition is over %d bytes", MaxBundleDefinitionBytes)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return nil, errors.New("the definition must be a JSON object")
	}
	out := &Parsed{}
	for k, raw := range top {
		switch {
		case k == "name":
			if err := json.Unmarshal(raw, &out.Name); err != nil {
				return nil, errors.New("name must be a string")
			}
		case k == "description":
			if err := json.Unmarshal(raw, &out.Description); err != nil {
				return nil, errors.New("description must be a string")
			}
		case k == "hooks":
		case strings.HasPrefix(k, "_"):
			// A comment block, as the Pix plugin's hooks.json has.
		default:
			return nil, fmt.Errorf("unknown key %q", clipKey(k))
		}
	}
	rawHooks, ok := top["hooks"]
	if !ok {
		return nil, errors.New("hooks is required")
	}
	var events map[string][]dialectGroup
	dec := json.NewDecoder(bytes.NewReader(rawHooks))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&events); err != nil || len(events) == 0 {
		return nil, errors.New("hooks must be an object of events with matcher groups")
	}
	var names []string
	for ev := range events {
		names = append(names, ev)
	}
	sort.Slice(names, func(i, j int) bool {
		return eventRank(names[i]) < eventRank(names[j]) || eventRank(names[i]) == eventRank(names[j]) && names[i] < names[j]
	})

	for _, ev := range names {
		belaiEvent, known := claudeEvents[ev]
		if !known {
			out.Notes = append(out.Notes, fmt.Sprintf("event %s is not one Belai runs, so it was skipped", clipKey(ev)))
			continue
		}
		for gi, g := range events[ev] {
			matcher, err := translateMatcher(g.Matcher, belaiEvent)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", ev, gi, err)
			}
			for hi, h := range g.Hooks {
				where := fmt.Sprintf("%s[%d].hooks[%d]", ev, gi, hi)
				if h.Type != "command" {
					out.Notes = append(out.Notes, fmt.Sprintf("%s is a %q handler, which Belai does not run, so it was skipped", where, clipKey(h.Type)))
					continue
				}
				if len(out.Hooks) >= MaxBundleHooks {
					return nil, fmt.Errorf("a bundle defines at most %d commands", MaxBundleHooks)
				}
				hook, err := bundleHook(bundle, dir, ev, belaiEvent, gi, hi, matcher, h, allowed)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", where, err)
				}
				out.Hooks = append(out.Hooks, hook)
			}
		}
	}
	return out, nil
}

func eventRank(ev string) int {
	for i, e := range claudeEventOrder {
		if e == ev {
			return i
		}
	}
	return len(claudeEventOrder)
}

func clipKey(s string) string {
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// translateMatcher turns a dialect matcher (a regular expression over tool
// names) into one of Belai's '|'-separated globs. Only forms with an exact
// glob equivalent are accepted, so a hook can never fire on fewer calls than it
// was written for; a hook can only deny or ask, so firing on more is safe.
func translateMatcher(m, event string) (string, error) {
	m = strings.TrimSpace(m)
	if len(m) > maxMatcherBytes {
		return "", fmt.Errorf("matcher is over %d bytes", maxMatcherBytes)
	}
	if m == "" || m == "*" || m == ".*" {
		return "", nil
	}
	var alts []string
	for _, a := range strings.Split(m, "|") {
		a = strings.TrimSpace(a)
		a = strings.TrimPrefix(a, "^")
		a = strings.TrimSuffix(a, "$")
		a = strings.ReplaceAll(a, ".*", "*")
		if a == "" || !matcherAltRE.MatchString(a) {
			return "", fmt.Errorf("matcher %q uses a regular expression Belai cannot narrow exactly", clipKey(m))
		}
		alts = append(alts, a)
	}
	return strings.Join(alts, "|"), nil
}

// bundleHook validates one command and builds its Hook.
func bundleHook(bundle, dir, claudeEvent, event string, gi, hi int, matcher string, h dialectHandler, allowed []string) (*Hook, error) {
	cmd := strings.TrimSpace(h.Command)
	if cmd == "" {
		return nil, errors.New("command is required")
	}
	if len(cmd) > maxCommandBytes {
		return nil, fmt.Errorf("command is over %d bytes", maxCommandBytes)
	}
	// Both spellings of the plugin root are the bundle itself. No other variable
	// is expanded: without a shell a `$` would reach the program as a literal.
	cmd = strings.ReplaceAll(cmd, "${CLAUDE_PLUGIN_ROOT}", ".")
	cmd = strings.ReplaceAll(cmd, "$CLAUDE_PLUGIN_ROOT", ".")
	argv, err := tokenize(cmd)
	if err != nil {
		return nil, err
	}
	allowedProgram, err := checkProgram(dir, argv[0], allowed)
	if err != nil {
		return nil, err
	}
	if err := checkArgs(dir, argv[1:], allowedProgram); err != nil {
		return nil, err
	}
	timeoutMS := DialectDefaultTimeoutMS
	if h.Timeout != nil {
		t := *h.Timeout
		if t <= 0 || math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, errors.New("timeout must be a positive number of seconds")
		}
		timeoutMS = int(math.Min(t*1000, MaxTimeoutMS))
	}
	return &Hook{
		Name:           fmt.Sprintf("bundle:%s:%s:%02d-%02d", bundle, claudeEvent, gi+1, hi+1),
		Event:          event,
		EventName:      claudeEvent,
		Command:        strings.Join(argv, " "),
		Matcher:        matcher,
		TimeoutMS:      timeoutMS,
		Dir:            dir,
		Bundle:         bundle,
		Dialect:        DialectClaude,
		Argv:           argv,
		AllowedProgram: allowedProgram,
	}, nil
}

// tokenize splits a command into words, honouring whole-word single and double
// quotes (no escapes), and refuses a word a shell would read as anything but text.
func tokenize(cmd string) ([]string, error) {
	var toks []string
	var cur strings.Builder
	var quote rune
	inTok := false
	flush := func() error {
		if !inTok {
			return nil
		}
		t := cur.String()
		if t == "" || !tokenRE.MatchString(t) {
			return fmt.Errorf("command word %q is not plain text", clipKey(t))
		}
		toks = append(toks, t)
		cur.Reset()
		inTok = false
		return nil
	}
	for _, r := range cmd {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inTok = true
		case unicode.IsSpace(r):
			if err := flush(); err != nil {
				return nil, err
			}
		default:
			cur.WriteRune(r)
			inTok = true
		}
	}
	if quote != 0 {
		return nil, errors.New("command has an unclosed quote")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, errors.New("command is required")
	}
	return toks, nil
}

// hasDotDot reports whether a path has a `..` segment.
func hasDotDot(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// checkProgram classifies the first word: a regular file inside the bundle
// (false), or a bare name the user allowed (true).
func checkProgram(dir, first string, allowed []string) (bool, error) {
	if filepath.IsAbs(first) || strings.HasPrefix(first, "~") || hasDotDot(first) {
		return false, fmt.Errorf("program %q must be a file inside the bundle or an allowed program name", clipKey(first))
	}
	if resolved, err := resolveIn(dir, first); err == nil {
		if fi, serr := os.Stat(resolved); serr == nil && fi.Mode().IsRegular() {
			return false, nil
		}
	}
	if strings.Contains(first, "/") {
		return false, fmt.Errorf("script %q is not a file in the bundle", clipKey(first))
	}
	for _, a := range allowed {
		if a == first && ValidProgramName(a) {
			if _, err := lookAllowed(dir, first); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, fmt.Errorf("program %q is not a file in the bundle and is not listed in hooks.allowed_programs", clipKey(first))
}

// lookAllowed finds an allowed program on PATH. A program found inside the
// bundle's own directory (or a relative PATH entry) is refused, so a bundle
// cannot shadow a program the user allowed by name.
func lookAllowed(dir, name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("program %q is not on PATH", name)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("program %q resolves to a relative path", name)
	}
	if real, rerr := filepath.EvalSymlinks(p); rerr == nil {
		p = real
	}
	root, rerr := filepath.EvalSymlinks(dir)
	if rerr != nil {
		root = dir
	}
	if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("program %q resolves inside the bundle", name)
	}
	return p, nil
}

// interpreterFlags are arguments that hand a program code to run.
var interpreterFlags = map[string]bool{"-c": true, "-e": true, "-E": true, "-p": true, "--eval": true, "--command": true, "--exec": true}

// checkArgs refuses an argument that names a path outside the bundle, so an
// allowed program cannot be pointed at another file, or handed code to run.
func checkArgs(dir string, args []string, allowedProgram bool) error {
	for _, a := range args {
		if allowedProgram && interpreterFlags[a] {
			return fmt.Errorf("argument %q would hand the program code to run", a)
		}
		v := a
		if i := strings.IndexByte(v, '='); i >= 0 && strings.HasPrefix(v, "-") {
			v = v[i+1:]
		}
		if v == "" {
			continue
		}
		if filepath.IsAbs(v) || strings.HasPrefix(v, "~") || hasDotDot(v) {
			return fmt.Errorf("argument %q names a path outside the bundle", clipKey(a))
		}
		if strings.Contains(v, "/") {
			if resolved, err := filepath.EvalSymlinks(filepath.Join(dir, v)); err == nil {
				root, rerr := filepath.EvalSymlinks(dir)
				if rerr != nil {
					root = dir
				}
				if rel, err := filepath.Rel(root, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					return fmt.Errorf("argument %q resolves outside the bundle", clipKey(a))
				}
			}
		}
	}
	return nil
}
