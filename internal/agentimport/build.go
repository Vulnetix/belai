package agentimport

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/skills"
)

// Format names a source format.
type Format string

// The formats Import reads.
const (
	Claws    Format = "claws"
	Nemoclaw Format = "nemoclaw"
	Hermes   Format = "hermes"
	MiniSWE  Format = "mini-swe"
)

// The formats of other harnesses' own files (see harness.go) and Belai's own
// file layouts. ImportItem reads them; Import and `belai agent import` do not.
const (
	Belai      Format = "belai"
	ClaudeCode Format = "claude-code"
	Cursor     Format = "cursor"
	Codex      Format = "codex"
	GeminiCLI  Format = "gemini-cli"
	OpenCode   Format = "opencode"
	Windsurf   Format = "windsurf"
	Copilot    Format = "copilot"
	Cline      Format = "cline"
	GenericMD  Format = "generic-md"
)

// Formats lists the formats in the order the help names them.
func Formats() []Format { return []Format{Claws, Nemoclaw, Hermes, MiniSWE} }

// NoteKind says what became of one piece of the source.
type NoteKind string

// Note kinds.
const (
	// Mapped: the source value became a field of the profile.
	Mapped NoteKind = "mapped"
	// Kept: the source value has no field and is held in the profile's metadata.
	Kept NoteKind = "metadata"
	// Dropped: the source value was left out, and the note says why.
	Dropped NoteKind = "dropped"
	// Warning: something the user should read before saving.
	Warning NoteKind = "warning"
)

// Note is one line of the import report. Text is composed by this package; any
// source value in it is cleaned and cut short.
type Note struct {
	Kind  NoteKind
	Field string
	Text  string
}

// Skill is a skill found beside the definition, already validated by the
// library's rules; Doc is its canonical SKILL.md.
type Skill struct {
	Name string
	Doc  []byte
}

// Result is a converted definition: the profile (validated), the skills that
// came with it, and the report.
type Result struct {
	Format Format
	// Kind, Name, Doc and SHA256 describe the converted item whatever its kind
	// (ImportItem fills them). Doc is the canonical bytes the library stores: the
	// document of a skill, prompt, command or JSON kind, the profile Markdown of
	// an agent, the crew JSON of a crew, the text of a document. SHA256 is the
	// hash of Doc.
	Kind   Kind
	Name   string
	Doc    []byte
	SHA256 string
	// Converted is true when the source was not already a Belai document and had
	// to be rewritten (front matter added, a key mapped or left out).
	Converted bool
	// Description is the item's own description (a command, prompt, skill, agent
	// or crew), one clean line.
	Description string
	// Files are the scripts a hook carries beside its document, in path order.
	// SHA256 of a hook is libitem.HashBundle over Doc and these files.
	Files []File
	// Profile and Skills are set for an agent.
	Profile agentprofile.AgentProfile
	Skills  []Skill
	Notes   []Note
	// MutatingTools are the profile's tools that can change files or run
	// commands, for the caller to show before it saves.
	MutatingTools []string
}

// Options adjusts an import.
type Options struct {
	// Name overrides the profile name derived from the source.
	Name string
	// Hook says where a hooks file lives and where its scripts may be read from.
	Hook HookContext
}

const (
	maxPrompt     = 32 << 10
	maxIterations = 200
	maxMetaLine   = 1000
	// metaBudget keeps the metadata inside the profile limit (16 KiB) with room.
	metaBudget = 15000
)

// readOnlyTools is what an imported definition gets when it names no tool Belai
// knows: reading and searching, never writing or running anything.
var readOnlyTools = []string{"Read", "Grep", "Glob"}

// mutatingTools are the table's targets that change files, run commands or start
// subagents.
var mutatingTools = map[string]bool{"Write": true, "Edit": true, "Bash": true, "Task": true}

// toolTable maps the names other harnesses give their tools to Belai's. Matching
// is on the lower-cased name with separators removed. A name that is not here is
// not imported, so a source can never name a tool into Belai by spelling.
var toolTable = map[string]string{
	"read": "Read", "readfile": "Read", "view": "Read", "cat": "Read", "open": "Read",
	"grep": "Grep", "search": "Grep", "searchfiles": "Grep", "ripgrep": "Grep", "rg": "Grep", "findinfiles": "Grep",
	"glob": "Glob", "find": "Glob", "list": "Glob", "listfiles": "Glob", "listdirectory": "Glob",
	"ls":    "LS",
	"write": "Write", "writefile": "Write", "create": "Write", "createfile": "Write",
	"edit": "Edit", "strreplace": "Edit", "strreplaceeditor": "Edit", "patch": "Edit", "applypatch": "Edit", "replace": "Edit", "multiedit": "Edit",
	"bash": "Bash", "shell": "Bash", "exec": "Bash", "run": "Bash", "runcommand": "Bash", "terminal": "Bash", "execute": "Bash", "command": "Bash", "sh": "Bash",
	"webfetch": "WebFetch", "fetch": "WebFetch", "httpget": "WebFetch", "openurl": "WebFetch",
	"websearch": "WebSearch",
	"todo":      "Todo", "todowrite": "Todo", "updateplan": "Todo", "plan": "Todo",
	"task": "Task", "subagent": "Task",
}

var toolKeyStrip = regexp.MustCompile(`[^a-z0-9]`)

func toolKey(s string) string { return toolKeyStrip.ReplaceAllString(strings.ToLower(s), "") }

// builder accumulates one import.
type builder struct {
	format Format
	opts   Options
	p      agentprofile.AgentProfile
	notes  []Note
	meta   map[string]string
	skills []Skill
	tools  []string
	// toolsSet is true when the source said which tools to use.
	toolsSet bool
	srcName  string
	// metaBytes is the size of the metadata so far.
	metaBytes int
}

func newBuilder(f Format, srcName string, o Options) *builder {
	b := &builder{format: f, opts: o, meta: map[string]string{}, srcName: srcName}
	b.p.Mode = agentprofile.ModeSingle
	b.p.Autonomy = agentprofile.AutonomySupervised
	b.metaSet("source.format", string(f))
	return b
}

func (b *builder) note(k NoteKind, field, format string, args ...any) {
	// A field or a value in a note may be a key or a path from the source, so the
	// note is cleaned of terminal escapes and control characters whatever made it.
	b.notes = append(b.notes, Note{Kind: k, Field: line(field, 120), Text: line(fmt.Sprintf(format, args...), 400)})
}

// line cleans a source value to one short line.
func line(s string, n int) string { return sanitize.Line(s, n) }

// metaSet keeps a source value in the profile's metadata under key, as one clean
// line. The key is the harness's; the value is cleaned and cut.
func (b *builder) metaSet(key, val string) bool {
	val = line(val, maxMetaLine)
	if val == "" {
		return false
	}
	if len(key) > 64 {
		key = key[:64]
	}
	old, exists := b.meta[key]
	if !exists && len(b.meta) >= 32 {
		b.note(Dropped, key, "not kept: a profile holds at most 32 metadata entries")
		return false
	}
	if b.metaBytes-len(old)+len(key)+len(val) > metaBudget {
		b.note(Dropped, key, "not kept: the metadata would be over %d bytes", metaBudget)
		return false
	}
	b.metaBytes += len(key) + len(val) - len(old)
	if exists {
		b.metaBytes -= len(key)
	}
	b.meta[key] = val
	return true
}

// keep records a source value in metadata and in the report.
func (b *builder) keep(field, key, val string) {
	if b.metaSet(key, val) {
		b.note(Kept, field, "kept in metadata as %s", key)
	}
}

func (b *builder) setName(raw string) {
	if b.opts.Name != "" {
		if profileName(b.opts.Name) != b.opts.Name {
			b.note(Warning, "name", "the name %q was changed to %q: a profile name is lower case letters, digits, dot, underscore and hyphen", line(b.opts.Name, 60), profileName(b.opts.Name))
		}
		raw = b.opts.Name
	}
	b.p.Name = profileName(raw)
	if b.p.Name == "" {
		b.p.Name = profileName(b.srcName)
	}
	if b.p.Name == "" {
		b.p.Name = "imported-agent"
	}
}

var nameStrip = regexp.MustCompile(`[^a-z0-9._-]+`)

// profileName makes a profile name from any source string: lower case, letters,
// digits, dot, underscore and hyphen, at most 64.
func profileName(s string) string {
	s = nameStrip.ReplaceAllString(strings.ToLower(sanitize.Line(s, 200)), "-")
	s = strings.Trim(s, ".-_")
	if len(s) > 64 {
		s = strings.Trim(s[:64], ".-_")
	}
	return s
}

func (b *builder) setDescription(s string, fallback string) {
	s = line(s, 300)
	if s == "" {
		s = fallback
	}
	b.p.Description = s
}

func (b *builder) setPrompt(s string) {
	s = strings.TrimSpace(sanitize.Text(s))
	if len(s) > maxPrompt {
		s = strings.TrimSpace(clipRunesBytes(s, maxPrompt))
		b.note(Warning, "system_prompt", "the instructions were cut at %d KiB", maxPrompt>>10)
	}
	b.p.SystemPrompt = s
}

func clipRunesBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isRuneStart(c byte) bool { return c&0xC0 != 0x80 }

// setModel maps "provider/model", or a bare model with its provider, onto the
// profile when the provider is one Belai has built in; otherwise the value is
// kept in metadata and the profile inherits the session's model.
func (b *builder) setModel(field, providerName, model string) {
	providerName, model = strings.TrimSpace(providerName), strings.TrimSpace(model)
	if model == "" {
		return
	}
	if providerName == "" {
		if p, m, ok := strings.Cut(model, "/"); ok {
			providerName, model = p, m
		}
	}
	if providerName != "" && provider.Builtin(providerName) && cleanModelID(model) {
		b.p.Provider, b.p.Model = providerName, model
		b.note(Mapped, field, "provider %s, model %s", providerName, line(model, 80))
		return
	}
	joined := model
	if providerName != "" {
		joined = providerName + "/" + model
	}
	b.keep(field, string(b.format)+".model", joined)
	b.note(Warning, field, "the provider is not one of Belai's built-in providers, so the profile uses the session's model")
}

var modelIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,127}$`)

func cleanModelID(m string) bool { return modelIDRE.MatchString(m) }

// addTools maps source tool names through the table. Names the table does not
// know are listed in the report and dropped. denied names are removed after.
func (b *builder) addTools(field string, names []string, denied []string) {
	b.toolsSet = true
	deny := map[string]bool{}
	for _, d := range denied {
		if t, ok := toolTable[toolKey(d)]; ok {
			deny[t] = true
		}
	}
	have := map[string]bool{}
	for _, t := range b.tools {
		have[t] = true
	}
	var unknown, unknownDeny []string
	for _, d := range denied {
		if _, ok := toolTable[toolKey(d)]; !ok {
			unknownDeny = append(unknownDeny, line(d, 40))
		}
	}
	for _, n := range names {
		t, ok := toolTable[toolKey(n)]
		switch {
		case !ok:
			unknown = append(unknown, line(n, 40))
		case deny[t]:
		case !have[t]:
			have[t] = true
			b.tools = append(b.tools, t)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		b.note(Dropped, field, "tools Belai has no match for: %s", strings.Join(capList(unknown, 12), ", "))
	}
	if len(unknownDeny) > 0 {
		sort.Strings(unknownDeny)
		// A denied tool Belai cannot name might be one of the tools just allowed, so
		// the tools that change things are held back rather than granted on a guess.
		kept := b.tools[:0]
		var held []string
		for _, t := range b.tools {
			if mutatingTools[t] {
				held = append(held, t)
				continue
			}
			kept = append(kept, t)
		}
		b.tools = kept
		b.note(Warning, field, "the source denies tools Belai has no name for (%s), so it cannot tell what they cover", strings.Join(capList(unknownDeny, 12), ", "))
		if len(held) > 0 {
			b.note(Warning, field, "%s left out for that reason; add them to the profile's tools yourself if you want them", strings.Join(held, ", "))
		}
	}
}

func capList(l []string, n int) []string {
	if len(l) <= n {
		return l
	}
	return append(append([]string{}, l[:n]...), fmt.Sprintf("and %d more", len(l)-n))
}

// finish checks the pieces and builds the result. The profile goes through the
// profile validator, which is the authority.
func (b *builder) finish() (Result, error) {
	if b.p.SystemPrompt == "" {
		return Result{}, errors.New("the definition holds no instructions to use as the system prompt")
	}
	if b.p.Name == "" {
		b.setName("")
	}
	if b.p.Description == "" {
		b.p.Description = "Imported " + string(b.format) + " agent"
	}
	tools := b.tools
	if len(tools) == 0 {
		tools = append([]string{}, readOnlyTools...)
		if b.toolsSet {
			b.note(Warning, "tools", "no tool in the source maps to one of Belai's, so the profile gets the read-only set (%s)", strings.Join(readOnlyTools, ", "))
		} else {
			b.note(Warning, "tools", "the source names no tools, so the profile gets the read-only set (%s) and not every tool", strings.Join(readOnlyTools, ", "))
		}
	}
	var mut []string
	for _, t := range tools {
		if mutatingTools[t] {
			mut = append(mut, t)
		}
	}
	if len(b.skills) > 0 {
		tools = appendOnce(tools, "Skill")
		for _, s := range b.skills {
			b.p.Skills = append(b.p.Skills, s.Name)
		}
	}
	b.p.Tools = tools
	if len(b.meta) > 0 {
		b.p.Metadata = b.meta
	}
	if err := b.p.Validate(); err != nil {
		return Result{}, fmt.Errorf("the converted profile is not valid: %w", err)
	}
	return Result{Format: b.format, Profile: b.p, Skills: b.skills, Notes: b.notes, MutatingTools: mut}, nil
}

func appendOnce(l []string, s string) []string {
	for _, x := range l {
		if x == s {
			return l
		}
	}
	return append(l, s)
}

// collectSkills validates each skills/<dir>/SKILL.md of the tree under root with
// the library's rules and adds the ones that pass. A skill that fails, or that
// would take a reserved name, is named in the report and left out.
func (b *builder) collectSkills(t *tree, root string) {
	prefix := root + "/"
	var dirs []string
	for _, n := range t.names() {
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, "/SKILL.md") && strings.Count(n[len(prefix):], "/") == 1 {
			dirs = append(dirs, n)
		}
	}
	for _, n := range dirs {
		it, err := libitem.Validate(libitem.Skill, t.files[n])
		if err != nil {
			b.note(Dropped, n, "skill not imported: %s", line(err.Error(), 160))
			continue
		}
		if skills.ReservedName(it.Name) {
			b.note(Dropped, n, "skill not imported: the name starts with %q, which is Belai's", skills.BuiltinPrefix)
			continue
		}
		if len(b.skills) >= agentprofile.MaxSkills {
			b.note(Dropped, n, "skill not imported: a profile names at most %d skills", agentprofile.MaxSkills)
			continue
		}
		dup := false
		for _, have := range b.skills {
			dup = dup || have.Name == it.Name
		}
		if dup {
			b.note(Dropped, n, "skill not imported: a skill named %s was already found", it.Name)
			continue
		}
		b.skills = append(b.skills, Skill{Name: it.Name, Doc: it.Doc})
		b.note(Mapped, n, "skill %s will be installed and named in the profile", it.Name)
	}
}

// noteSkipped reports the files the reader left out, whatever the format.
func (b *builder) noteSkipped(t *tree) {
	if t.skipped > 0 {
		b.note(Dropped, "files", "%d file(s) were not read: links, special files, credential files, files over %d bytes and unreadable ones", t.skipped, maxFileBytes)
	}
}
