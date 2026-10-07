package agentimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agentfiles"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
)

// Hooks are the one kind that carries files. A hook item is the `hooks` block of
// a coding agent's settings (Claude Code's settings.json, Codex's hooks.json) as
// the library's document, plus the scripts its commands run, in a bundle
// (libitem.HashBundle over both).
//
// The reader is built for a file that holds credentials:
//
//   - A settings file is decoded into a map of raw values and only its `hooks` key
//     is looked at. No other key is parsed, kept, logged or named in a report.
//   - A report names a command by its first word only. A command can hold a
//     secret in an argument, so the full line never leaves this function except
//     inside the document an import uploads, and that document passes the
//     secret gate (agentfiles.HoldsSecret) first.
//   - A script is carried only when the first word of a command is a file under
//     the harness's own directories (or the trusted repository): a regular file,
//     no symbolic link on the way, text, within the bundle limits, and holding no
//     secret. A script is read here and never run.
//   - A program (a bare name) or a path outside those directories stays as it is,
//     with a warning. A script that exists but cannot be carried (a link, too
//     large, binary, holding a secret) makes the item invalid.

// File is one script a hook bundle carries: a path relative to the bundle
// directory, its bytes and their SHA-256.
type File struct {
	Path   string
	Data   []byte
	SHA256 string
}

// HookContext tells the hook reader where a hooks file lives, so it can find the
// scripts the commands name. Every field is chosen by the caller (internal/libscan)
// from the host's own registry and trusted repositories, never from a request.
type HookContext struct {
	// Home resolves a leading ~ and $HOME.
	Home string
	// RepoRoot resolves $CLAUDE_PROJECT_DIR, and a relative script of a project
	// settings file. It is empty for a user-scope file.
	RepoRoot string
	// Roots are the absolute directories a script may be read from. With none, no
	// script is carried and every command is reported as it stands.
	Roots []string
	// Settings is true when the hooks are the `hooks` key of a settings file (the
	// rest of the file is left alone); false when the file is a hooks file.
	Settings bool
}

// maxHookCommands is what one bundle may define (hooks.MaxBundleHooks).
const maxHookCommands = hooks.MaxBundleHooks

var hookEnvAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shellSyntax are characters a bundle command cannot carry: the host runs a
// command as a fixed argv, never through a shell.
const shellSyntax = "$~`;|&<>(){}*?[]\\!#"

// importHook reads one hooks file of the claude-code or codex dialect, or one of
// Belai's own bundles, and converts it.
func importHook(path string, t *tree, f Format, o Options) (Result, error) {
	if f == "" {
		f = ClaudeCode
	}
	switch f {
	case ClaudeCode, Codex:
	case Belai:
		return importBelaiHook(path, t, o)
	default:
		return Result{}, fmt.Errorf("%s hooks are not read (only claude-code, codex and belai)", f)
	}
	_, data, err := t.only()
	if err != nil {
		return Result{}, err
	}
	top, err := hookTop(data)
	if err != nil {
		return Result{}, err
	}
	ctx := o.Hook
	n := &noter{}
	description := ""
	if !ctx.Settings {
		// A hooks file of its own: a comment key and the Codex description are
		// expected; any other key is counted, never named.
		other := 0
		for _, k := range sortedRaw(top) {
			switch {
			case k == "hooks":
			case k == "description":
				var s string
				if err := json.Unmarshal(top[k], &s); err != nil {
					return Result{}, errors.New("description must be a string")
				}
				description = strings.TrimSpace(s)
			case strings.HasPrefix(k, "_"):
				n.add(Dropped, line(k, 64), "comment key dropped")
			default:
				other++
			}
		}
		if other > 0 {
			n.add(Dropped, "keys", "%d other top-level key(s) have no place in a hook and were not imported", other)
		}
	}
	rawHooks, ok := top["hooks"]
	if !ok {
		return Result{}, errors.New("the file has no hooks key")
	}
	var events map[string]json.RawMessage
	if err := json.Unmarshal(rawHooks, &events); err != nil || events == nil {
		return Result{}, errors.New("hooks must be an object of events")
	}
	cv := &hookConverter{ctx: ctx, file: path, notes: n, names: map[string]string{}, used: map[string]bool{}, droppedKeys: map[string]bool{}}
	out := map[string]any{}
	for _, ev := range sortedRaw(events) {
		if strings.HasPrefix(ev, "_") {
			n.add(Dropped, line(ev, 64), "comment key dropped")
			continue
		}
		if !libitem.HookEvent(ev) {
			return Result{}, fmt.Errorf("%s is not an event a hook can name", line(ev, 40))
		}
		groups, err := cv.groups(ev, events[ev])
		if err != nil {
			return Result{}, err
		}
		out[ev] = groups
	}
	if len(out) == 0 {
		return Result{}, errors.New("hooks names no event")
	}
	name := o.Name
	if name == "" {
		name = "imported-hooks"
	}
	docMap := map[string]any{"name": name, "hooks": out}
	if description != "" {
		docMap["description"] = description
	}
	raw, err := json.Marshal(docMap)
	if err != nil {
		return Result{}, err
	}
	item, err := libitem.Validate(libitem.Hook, raw)
	if err != nil {
		return Result{}, err
	}
	if agentfiles.HoldsSecret(item.Doc) {
		return Result{}, errors.New("the hooks document looks like it holds a secret, so it is not imported")
	}
	if err := libstore.UntrustedText(item.Doc); err != nil {
		return Result{}, err
	}
	files := cv.files
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	n.add(Mapped, "hooks", "%d command(s) in %d event(s), %d script(s) carried", cv.commands, len(out), len(files))
	return Result{
		Format: f, Name: item.Name, Doc: item.Doc, Files: files, SHA256: bundleHash(item.Doc, files),
		Description: line(description, 300), Notes: n.list, Converted: true,
	}, nil
}

// ListHooks reports whether a file holds hooks to import: a non-empty `hooks`
// object. settings is true for a settings file, where a file that is not JSON
// simply holds none; a hooks file that is not JSON is a candidate, so the scan
// can say why it is invalid. Only the hooks key is looked at.
func ListHooks(path string, settings bool) (bool, error) {
	t, err := loadItem(path, KindHook)
	if err != nil {
		return false, err
	}
	_, data, err := t.only()
	if err != nil {
		return false, err
	}
	top, err := hookTop(data)
	if err != nil {
		return !settings, nil
	}
	raw, ok := top["hooks"]
	if !ok {
		return false, nil
	}
	var events map[string]json.RawMessage
	if json.Unmarshal(raw, &events) != nil {
		return !settings, nil
	}
	return len(events) > 0, nil
}

// hookTop decodes the top-level object of a hooks or settings file into raw
// values, so nothing but what is asked for is ever parsed.
func hookTop(data []byte) (map[string]json.RawMessage, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return nil, errors.New("the file is not a JSON object")
	}
	return top, nil
}

func sortedRaw(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func bundleHash(doc []byte, files []File) string {
	bf := make([]libitem.BundleFile, len(files))
	for i, f := range files {
		bf[i] = libitem.BundleFile{Path: f.Path, SHA256: f.SHA256}
	}
	return libitem.HashBundle(doc, bf)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// hookConverter holds what one conversion carries from group to group.
type hookConverter struct {
	ctx   HookContext
	file  string
	notes *noter

	commands    int
	files       []File
	total       int
	names       map[string]string // absolute script path -> bundle name
	used        map[string]bool   // bundle names taken
	droppedKeys map[string]bool
}

// groups converts one event's list of matcher groups.
func (cv *hookConverter) groups(ev string, raw json.RawMessage) ([]map[string]any, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return nil, fmt.Errorf("%s must be a list of at least one group", line(ev, 40))
	}
	if len(list) > libitem.MaxHookGroups {
		return nil, fmt.Errorf("%s has %d groups; the most is %d", line(ev, 40), len(list), libitem.MaxHookGroups)
	}
	var out []map[string]any
	for gi, g := range list {
		var group map[string]json.RawMessage
		if err := json.Unmarshal(g, &group); err != nil || group == nil {
			return nil, fmt.Errorf("%s group %d must be an object", line(ev, 40), gi+1)
		}
		outGroup := map[string]any{}
		for _, k := range sortedRaw(group) {
			switch k {
			case "matcher":
				var m string
				if err := json.Unmarshal(group[k], &m); err != nil {
					return nil, fmt.Errorf("%s group %d: matcher must be a string", line(ev, 40), gi+1)
				}
				if m != "" && !libitem.HookTakesMatcher(ev) {
					return nil, fmt.Errorf("%s takes no matcher, and group %d has one", line(ev, 40), gi+1)
				}
				outGroup["matcher"] = m
			case "hooks":
			default:
				cv.drop(k)
			}
		}
		var handlers []json.RawMessage
		if err := json.Unmarshal(group["hooks"], &handlers); err != nil || len(handlers) == 0 {
			return nil, fmt.Errorf("%s group %d must list at least one handler", line(ev, 40), gi+1)
		}
		if len(handlers) > libitem.MaxHookHandlers {
			return nil, fmt.Errorf("%s group %d has %d handlers; the most is %d", line(ev, 40), gi+1, len(handlers), libitem.MaxHookHandlers)
		}
		var outHandlers []map[string]any
		for hi, h := range handlers {
			oh, err := cv.handler(ev, gi, hi, h)
			if err != nil {
				return nil, err
			}
			outHandlers = append(outHandlers, oh)
		}
		outGroup["hooks"] = outHandlers
		out = append(out, outGroup)
	}
	return out, nil
}

// drop notes a key Belai has no field for, once per key name. The names are the
// format's own (statusMessage, async), never a value.
func (cv *hookConverter) drop(key string) {
	if cv.droppedKeys[key] {
		return
	}
	cv.droppedKeys[key] = true
	if strings.HasPrefix(key, "_") {
		cv.notes.add(Dropped, line(key, 64), "comment key dropped")
		return
	}
	cv.notes.add(Dropped, line(key, 64), "not imported: a key Belai has no field for")
}

// handler converts one handler. Only a command handler is a hook.
func (cv *hookConverter) handler(ev string, gi, hi int, raw json.RawMessage) (map[string]any, error) {
	where := fmt.Sprintf("%s group %d handler %d", line(ev, 40), gi+1, hi+1)
	var h map[string]json.RawMessage
	if err := json.Unmarshal(raw, &h); err != nil || h == nil {
		return nil, fmt.Errorf("%s must be an object", where)
	}
	var typ string
	if err := json.Unmarshal(h["type"], &typ); err != nil {
		return nil, fmt.Errorf("%s: type must be a string", where)
	}
	if typ != "command" {
		return nil, fmt.Errorf("%s is a %q handler; only command handlers can be imported", where, line(typ, 24))
	}
	var cmd string
	if err := json.Unmarshal(h["command"], &cmd); err != nil {
		return nil, fmt.Errorf("%s: command must be a string", where)
	}
	cmd = strings.TrimSpace(cmd)
	switch {
	case cmd == "":
		return nil, fmt.Errorf("%s: command is empty", where)
	case len(cmd) > libitem.MaxHookCommand:
		return nil, fmt.Errorf("%s: command is %d bytes; the most is %d", where, len(cmd), libitem.MaxHookCommand)
	case strings.ContainsAny(cmd, "\n\r"):
		return nil, fmt.Errorf("%s: command spans more than one line", where)
	}
	cv.commands++
	if cv.commands > maxHookCommands {
		return nil, fmt.Errorf("a hook defines at most %d commands", maxHookCommands)
	}
	out := map[string]any{"type": "command"}
	for _, k := range sortedRaw(h) {
		switch k {
		case "type", "command":
		case "timeout":
			out["timeout"] = h[k]
		default:
			cv.drop(k)
		}
	}
	newCmd, err := cv.command(cmd)
	if err != nil {
		return nil, err
	}
	out["command"] = newCmd
	return out, nil
}

// command resolves the first word of a command and returns the command to keep:
// with the first word replaced by the bundle name of a script that is carried,
// otherwise as it stands. Every note names the first word and nothing else.
func (cv *hookConverter) command(cmd string) (string, error) {
	word, rest, ok := splitFirstWord(cmd)
	if !ok {
		cv.notes.add(Warning, "command", "a command has an unclosed quote, so it cannot run")
		return cmd, nil
	}
	if hookEnvAssign.MatchString(word) {
		cv.notes.add(Warning, "command", "a command starts with an environment assignment, which a bundle cannot run")
		return cmd, nil
	}
	shown := cv.show(word)
	if strings.ContainsAny(word, "|&;<>()`") || strings.Contains(word, "$(") {
		cv.notes.add(Warning, "command", "%s: the command uses shell syntax, which a bundle cannot run", shown)
		return cmd, nil
	}
	cands, isPath := cv.candidates(word)
	if !isPath {
		cv.programNote(word, shown)
		cv.argsNote(shown, rest)
		return cmd, nil
	}
	if len(cands) == 0 {
		cv.notes.add(Warning, "command", "%s is outside this harness's directories, so it is not carried", shown)
		cv.argsNote(shown, rest)
		return cmd, nil
	}
	for _, c := range cands {
		data, found, err := cv.readScript(c)
		if err != nil {
			return "", fmt.Errorf("script %s: %w", cv.show(word), err)
		}
		if !found {
			continue
		}
		name, err := cv.carry(c.abs, data)
		if err != nil {
			return "", err
		}
		cv.notes.add(Mapped, "command", "%s carried as %s", shown, name)
		cv.argsNote(shown, rest)
		return name + rest, nil
	}
	cv.notes.add(Warning, "command", "%s was not found, so it is not carried", shown)
	cv.argsNote(shown, rest)
	return cmd, nil
}

func (cv *hookConverter) programNote(word, shown string) {
	switch {
	case hooks.ValidProgramName(word):
		cv.notes.add(Warning, "command", "%s is not a bundle file; add it to hooks.allowed_programs on the host", shown)
	default:
		cv.notes.add(Warning, "command", "%s is not a name a bundle can call", shown)
	}
}

// argsNote warns once about an argument a bundle command cannot hold. It names
// the first word, never the argument.
func (cv *hookConverter) argsNote(shown, rest string) {
	if strings.ContainsAny(rest, shellSyntax) {
		cv.notes.add(Warning, "command", "%s: the arguments use shell syntax, which a bundle cannot run", shown)
	}
}

// show is a first word as a report names it: the home directory as ~, one clean
// line, cut short.
func (cv *hookConverter) show(word string) string {
	if cv.ctx.Home != "" && (word == cv.ctx.Home || strings.HasPrefix(word, cv.ctx.Home+"/")) {
		word = "~" + strings.TrimPrefix(word, cv.ctx.Home)
	}
	return line(word, 80)
}

// splitFirstWord reads the first shell word of a command, with whole-word quotes
// removed, and returns the rest of the command exactly as written.
func splitFirstWord(cmd string) (word, rest string, ok bool) {
	var b strings.Builder
	var quote byte
	i := 0
	for ; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				b.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ' ' || c == '\t':
			return b.String(), cmd[i:], true
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), "", quote == 0
}

// scriptCandidate is one place a script could be: an absolute path and the root
// it is under.
type scriptCandidate struct {
	abs  string
	root string
}

// candidates lists where a first word could be a file, in order, keeping those
// under a root. isPath is false for a bare word (a program on PATH, which is
// never read as a file).
func (cv *hookConverter) candidates(word string) (cands []scriptCandidate, isPath bool) {
	var bases []string
	rel := ""
	switch {
	case strings.HasPrefix(word, "~/") && cv.ctx.Home != "":
		bases, rel = []string{cv.ctx.Home}, word[2:]
	case strings.HasPrefix(word, "$HOME/") && cv.ctx.Home != "":
		bases, rel = []string{cv.ctx.Home}, word[len("$HOME/"):]
	case strings.HasPrefix(word, "${HOME}/") && cv.ctx.Home != "":
		bases, rel = []string{cv.ctx.Home}, word[len("${HOME}/"):]
	case strings.HasPrefix(word, "$CLAUDE_PROJECT_DIR/") && cv.ctx.RepoRoot != "":
		bases, rel = []string{cv.ctx.RepoRoot}, word[len("$CLAUDE_PROJECT_DIR/"):]
	case strings.HasPrefix(word, "${CLAUDE_PROJECT_DIR}/") && cv.ctx.RepoRoot != "":
		bases, rel = []string{cv.ctx.RepoRoot}, word[len("${CLAUDE_PROJECT_DIR}/"):]
	case strings.HasPrefix(word, "$CLAUDE_PLUGIN_ROOT/"):
		bases, rel = []string{filepath.Dir(cv.file), filepath.Dir(filepath.Dir(cv.file))}, word[len("$CLAUDE_PLUGIN_ROOT/"):]
	case strings.HasPrefix(word, "${CLAUDE_PLUGIN_ROOT}/"):
		bases, rel = []string{filepath.Dir(cv.file), filepath.Dir(filepath.Dir(cv.file))}, word[len("${CLAUDE_PLUGIN_ROOT}/"):]
	case strings.HasPrefix(word, "/"):
		bases, rel = []string{"/"}, word[1:]
	case strings.Contains(word, "/") && !strings.ContainsAny(word, "$~"):
		bases = []string{filepath.Dir(cv.file)}
		if cv.ctx.RepoRoot != "" {
			bases = append(bases, cv.ctx.RepoRoot)
		}
		rel = word
	case strings.ContainsAny(word, "/$~"):
		// A variable or ~ this reader does not resolve: a path, never carried.
		return nil, true
	default:
		return nil, false
	}
	if rel == "" || strings.ContainsRune(rel, 0) {
		return nil, true
	}
	for _, b := range bases {
		abs := filepath.Clean(filepath.Join(b, filepath.FromSlash(rel)))
		if r := cv.rootOf(abs); r != "" {
			cands = append(cands, scriptCandidate{abs: abs, root: r})
		}
	}
	return cands, true
}

// rootOf returns the root a path is lexically under, or "".
func (cv *hookConverter) rootOf(abs string) string {
	for _, r := range cv.ctx.Roots {
		r = filepath.Clean(r)
		if !filepath.IsAbs(r) {
			continue
		}
		if rel, err := filepath.Rel(r, abs); err == nil && rel != "." && filepath.IsLocal(rel) {
			return r
		}
	}
	return ""
}

// readScript reads a script a command names. found is false when nothing is at
// the path. A file that is there and cannot be carried is an error.
func (cv *hookConverter) readScript(c scriptCandidate) (data []byte, found bool, err error) {
	rel, _ := filepath.Rel(c.root, c.abs)
	cur := c.root
	segs := strings.Split(filepath.ToSlash(rel), "/")
	var fi fs.FileInfo
	for i, s := range segs {
		cur = filepath.Join(cur, s)
		fi, err = os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || isNotDir(err) {
				return nil, false, nil
			}
			return nil, false, errors.New("it cannot be read")
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return nil, false, errors.New("it goes through a symbolic link, so it is not carried")
		}
		if i < len(segs)-1 && !fi.IsDir() {
			return nil, false, nil
		}
	}
	switch {
	case !fi.Mode().IsRegular():
		return nil, false, errors.New("it is not a regular file")
	case fi.Size() == 0:
		return nil, false, errors.New("it is empty")
	case fi.Size() > libstore.MaxBundleFileBytes:
		return nil, false, fmt.Errorf("it is over %d bytes", libstore.MaxBundleFileBytes)
	}
	data, err = os.ReadFile(c.abs)
	if err != nil {
		return nil, false, errors.New("it cannot be read")
	}
	if len(data) > libstore.MaxBundleFileBytes || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return nil, false, errors.New("it is not bounded text, so it is not carried")
	}
	return data, true, nil
}

func isNotDir(err error) bool {
	var pe *fs.PathError
	return errors.As(err, &pe) && strings.Contains(pe.Err.Error(), "not a directory")
}

// carry adds a script to the bundle under a plain name and returns the name. The
// same file named twice is carried once; two files with one base name get
// distinct names.
func (cv *hookConverter) carry(abs string, data []byte) (string, error) {
	if name, ok := cv.names[abs]; ok {
		return name, nil
	}
	name := cv.freeName(bundleBase(filepath.Base(abs)))
	if len(cv.files) >= libstore.MaxBundleFiles {
		return "", fmt.Errorf("the commands name more than %d scripts", libstore.MaxBundleFiles)
	}
	if cv.total += len(data); cv.total > libstore.MaxBundleBytes {
		return "", fmt.Errorf("the scripts are over %d bytes in all", libstore.MaxBundleBytes)
	}
	if agentfiles.HoldsSecret(data) {
		return "", fmt.Errorf("script %s looks like it holds a secret, so it is not imported", name)
	}
	if err := libstore.UntrustedText(data); err != nil {
		return "", fmt.Errorf("script %s: %w", name, err)
	}
	cv.names[abs] = name
	cv.used[name] = true
	cv.files = append(cv.files, File{Path: name, Data: data, SHA256: sum(data)})
	return name, nil
}

var bundleSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)

// bundleBase turns a file name into one a bundle accepts: letters, digits and
// . _ -, not starting with a dot, at most 64 bytes, and never hooks.json.
func bundleBase(base string) string {
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" || !bundleSegment.MatchString(s) {
		s = "_" + strings.TrimLeft(s, ".")
	}
	if len(s) > 64 {
		s = s[:64]
	}
	if s == libitem.HookDefinitionFile {
		s = "script-" + s
	}
	return s
}

// freeName returns name, or name with -2, -3 and so on before its extension, that
// no other script has taken.
func (cv *hookConverter) freeName(name string) string {
	if !cv.used[name] {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		s := stem
		if len(s)+len(suffix)+len(ext) > 64 {
			s = s[:64-len(suffix)-len(ext)]
		}
		if cand := s + suffix + ext; !cv.used[cand] {
			return cand
		}
	}
}

// importBelaiHook reads one of Belai's own bundles: <hooks dir>/<name>/hooks.json
// and the files beside it, under the bounds an install holds a bundle to.
func importBelaiHook(path string, t *tree, o Options) (Result, error) {
	_, data, err := t.only()
	if err != nil {
		return Result{}, err
	}
	if filepath.Base(path) != libitem.HookDefinitionFile {
		return Result{}, fmt.Errorf("a Belai hook is the %s of its bundle", libitem.HookDefinitionFile)
	}
	if len(data) > hooks.MaxBundleDefinitionBytes {
		return Result{}, fmt.Errorf("%s is over %d bytes", libitem.HookDefinitionFile, hooks.MaxBundleDefinitionBytes)
	}
	item, err := libitem.Validate(libitem.Hook, data)
	if err != nil {
		return Result{}, err
	}
	dir := filepath.Dir(path)
	if base := filepath.Base(dir); base != item.Name {
		return Result{}, fmt.Errorf("the directory is named %s but the hook is named %s", line(base, 64), line(item.Name, 64))
	}
	if agentfiles.HoldsSecret(item.Doc) {
		return Result{}, errors.New("the hooks document looks like it holds a secret, so it is not imported")
	}
	if err := libstore.UntrustedText(item.Doc); err != nil {
		return Result{}, err
	}
	bf, why := libstore.ReadBundleDir(dir)
	if why != "" {
		return Result{}, errors.New(line(why, 200))
	}
	files := make([]File, len(bf))
	for i, b := range bf {
		if agentfiles.HoldsSecret(b.Data) {
			return Result{}, fmt.Errorf("script %s looks like it holds a secret, so it is not imported", line(b.Path, 64))
		}
		if err := libstore.UntrustedText(b.Data); err != nil {
			return Result{}, fmt.Errorf("script %s: %w", line(b.Path, 64), err)
		}
		files[i] = File{Path: b.Path, Data: b.Data, SHA256: b.SHA256}
	}
	n := &noter{}
	n.add(Mapped, "hooks", "a Belai hook bundle, kept as it is, %d script(s)", len(files))
	desc := ""
	var m map[string]any
	if json.Unmarshal(item.Doc, &m) == nil {
		if s, ok := m["description"].(string); ok {
			desc = line(s, 300)
		}
	}
	return Result{Format: Belai, Name: item.Name, Doc: item.Doc, Files: files, SHA256: bundleHash(item.Doc, files), Description: desc, Notes: n.list}, nil
}
