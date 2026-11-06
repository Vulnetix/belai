package shellsafe

import (
	"path"
	"strings"
)

// Hazard identifiers. The set is closed: docs/hazard-rails.md lists exactly
// these, and a test holds the page and [HazardIDs] together. An identifier is
// harness text, never a fragment of the command that matched.
const (
	// HazardDeleteCatastrophic is a recursive delete of the filesystem root, the
	// home directory, a top-level system directory or the working directory.
	HazardDeleteCatastrophic = "exec.delete.catastrophic"
	// HazardDiskRaw writes a raw device or lays down a new filesystem.
	HazardDiskRaw = "exec.disk.raw"
	// HazardFetchExec runs what a download returns without a file in between.
	HazardFetchExec = "exec.fetch_exec"
	// HazardPrivilege asks for another identity: sudo, doas, su, pkexec, or a
	// setuid bit.
	HazardPrivilege = "exec.privilege"
	// HazardGitDestructive is a git command that discards work or rewrites
	// shared history.
	HazardGitDestructive = "exec.git.destructive"
	// HazardOpaque is a line the harness could not read: it does not parse, is
	// oversized, holds a payload that cannot be seen into, or names its program
	// or a destructive argument through an expansion.
	HazardOpaque = "hazard.opaque"
)

// HazardIDs lists every identifier [Hazards] can return, in the order it
// returns them.
var HazardIDs = []string{
	HazardDeleteCatastrophic,
	HazardDiskRaw,
	HazardFetchExec,
	HazardPrivilege,
	HazardGitDestructive,
	HazardOpaque,
}

// Hazard is one reason a command line always asks.
type Hazard struct {
	ID string
}

// Hazards reports which hazards a parsed command line holds. It reads the
// commands [Analyze] found (written and unwrapped) and never splits the text
// again, so quoting, wrappers, sh -c, env, sudo, xargs and find -exec are seen
// through exactly as the permission rules see them. A line that is empty has
// none; a line that is not readable at all is [HazardOpaque], so the answer
// fails closed. The result is in [HazardIDs] order with no repeats.
func Hazards(a *Analysis) []Hazard {
	if a == nil {
		return []Hazard{{ID: HazardOpaque}}
	}
	if a.Source == "" {
		return nil
	}
	hit := map[string]bool{}
	if !a.Parseable || a.Has(FlagOpaque) {
		hit[HazardOpaque] = true
	}
	var fetch, sink, procSink, substSink bool
	for _, c := range a.Commands {
		if len(c.Argv) == 0 {
			continue
		}
		if expands(c.Argv[0]) {
			hit[HazardOpaque] = true
			continue
		}
		name := baseName(c.Argv[0])
		args := c.Argv[1:]
		switch {
		case name == "rm":
			rmHazards(args, hit)
		case name == "find":
			findHazards(args, hit)
		case name == "dd":
			ddHazards(args, hit)
		case rawDiskTools[name] || strings.HasPrefix(name, "mkfs"):
			hit[HazardDiskRaw] = true
		case name == "shred" || name == "tee":
			if touchesDevice(args) {
				hit[HazardDiskRaw] = true
			}
		case privilegeTools[name]:
			hit[HazardPrivilege] = true
		case name == "chmod":
			if setsSetuid(args) {
				hit[HazardPrivilege] = true
			}
		case name == "git":
			gitHazards(c.Argv, hit)
		}
		if fetchers[name] {
			fetch = true
		}
		if readsStdin(name, args) {
			sink = true
		}
		if isInterpreter(name) {
			procSink = true
		}
		if runsPayload(name, args) {
			substSink = true
		}
	}
	if fetch {
		switch {
		case a.Has(FlagPipe) && sink:
			hit[HazardFetchExec] = true
		case a.Has(FlagProcSubst) && procSink:
			hit[HazardFetchExec] = true
		case a.Has(FlagCmdSubst) && substSink:
			hit[HazardFetchExec] = true
		}
	}
	var out []Hazard
	for _, id := range HazardIDs {
		if hit[id] {
			out = append(out, Hazard{ID: id})
		}
	}
	return out
}

// expands reports a word the shell computes: a variable, a substitution or a
// backtick.
func expands(w string) bool { return strings.ContainsAny(w, "$`") }

// unquote removes one pair of surrounding quotes from the source text of a
// dynamic word, so "$HOME" is compared as $HOME.
func unquote(w string) string {
	if len(w) >= 2 && (w[0] == '"' && w[len(w)-1] == '"' || w[0] == '\'' && w[len(w)-1] == '\'') {
		return w[1 : len(w)-1]
	}
	return w
}

// options splits args at "--" into option words and operands.
func options(args []string) (opts, operands []string) {
	for i, a := range args {
		if a == "--" {
			return opts, append(operands, args[i+1:]...)
		}
		if len(a) > 1 && a[0] == '-' {
			opts = append(opts, a)
			continue
		}
		operands = append(operands, a)
	}
	return opts, operands
}

// shortFlag reports a short option letter in any cluster ("-rf" holds r).
func shortFlag(opts []string, letters string) bool {
	for _, o := range opts {
		if strings.HasPrefix(o, "--") {
			continue
		}
		if strings.ContainsAny(o[1:], letters) {
			return true
		}
	}
	return false
}

func hasLong(opts []string, names ...string) bool {
	for _, o := range opts {
		for _, n := range names {
			if o == n || strings.HasPrefix(o, n+"=") {
				return true
			}
		}
	}
	return false
}

// systemDirs are the top-level directories whose loss is the machine's.
var systemDirs = map[string]bool{
	"bin": true, "boot": true, "dev": true, "etc": true, "home": true, "lib": true, "lib32": true,
	"lib64": true, "media": true, "mnt": true, "opt": true, "proc": true, "root": true, "run": true,
	"sbin": true, "srv": true, "sys": true, "usr": true, "var": true,
	"Users": true, "System": true, "Library": true, "Applications": true, "private": true,
}

// catastrophic reports a delete target that is the root, the home directory, a
// top-level system directory or the working directory itself.
func catastrophic(t string) bool {
	switch unquote(strings.TrimSpace(t)) {
	case "*", ".*", "./*", ".", "./", "..", "../", "../*":
		return true
	}
	return wholeMachine(t)
}

// wholeMachine is catastrophic without the working directory forms: the root,
// the home directory or a top-level system directory, with or without a glob.
func wholeMachine(t string) bool {
	t = unquote(strings.TrimSpace(t))
	switch t {
	case "~", "$HOME", "${HOME}", "~/", "$HOME/", "${HOME}/", "~/*", "$HOME/*", "${HOME}/*", "~/.*", "$HOME/.*":
		return true
	}
	if !strings.HasPrefix(t, "/") {
		return false
	}
	glob := strings.HasSuffix(t, "/*") || strings.HasSuffix(t, "/.*")
	if glob {
		t = t[:strings.LastIndex(t, "/")]
		if t == "" {
			t = "/"
		}
	}
	c := path.Clean(t)
	if c == "/" {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(c, "/"), "/")
	return len(parts) == 1 && systemDirs[parts[0]]
}

func rmHazards(args []string, hit map[string]bool) {
	opts, operands := options(args)
	recursive := shortFlag(opts, "rR") || hasLong(opts, "--recursive", "--no-preserve-root")
	for _, t := range operands {
		switch {
		case catastrophic(t) && (recursive || wholeMachine(t)):
			hit[HazardDeleteCatastrophic] = true
		case recursive && expands(t):
			hit[HazardOpaque] = true
		}
	}
}

// findHazards covers find <root> -delete, the form of rm -rf that find spells
// itself. find -exec rm is seen by the unwrapper as an rm command.
func findHazards(args []string, hit map[string]bool) {
	del := false
	for _, a := range args {
		if a == "-delete" {
			del = true
		}
	}
	if !del {
		return
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") || a == "(" || a == "!" {
			break
		}
		switch {
		case wholeMachine(a):
			hit[HazardDeleteCatastrophic] = true
		case expands(a):
			hit[HazardOpaque] = true
		}
	}
}

// harmlessDevices are the device files a write cannot hurt.
var harmlessDevices = map[string]bool{
	"/dev/null": true, "/dev/zero": true, "/dev/stdout": true, "/dev/stderr": true, "/dev/stdin": true,
	"/dev/tty": true, "/dev/full": true, "/dev/random": true, "/dev/urandom": true,
}

func rawDevice(p string) bool {
	p = unquote(p)
	if !strings.HasPrefix(p, "/dev/") || harmlessDevices[p] || strings.HasPrefix(p, "/dev/fd/") {
		return false
	}
	return true
}

func ddHazards(args []string, hit map[string]bool) {
	for _, a := range args {
		if !strings.HasPrefix(a, "of=") {
			continue
		}
		dest := a[len("of="):]
		switch {
		case rawDevice(dest):
			hit[HazardDiskRaw] = true
		case expands(dest):
			hit[HazardOpaque] = true
		}
	}
}

// touchesDevice reports an operand that is a raw device.
func touchesDevice(args []string) bool {
	_, operands := options(args)
	found := false
	for _, o := range operands {
		if rawDevice(o) {
			found = true
		}
	}
	return found
}

// rawDiskTools lay down or change partitions and filesystems on a device.
var rawDiskTools = map[string]bool{
	"mke2fs": true, "mkswap": true, "wipefs": true, "fdisk": true, "sfdisk": true, "cfdisk": true,
	"parted": true, "gdisk": true, "sgdisk": true, "blkdiscard": true,
}

// privilegeTools run a command as another identity.
var privilegeTools = map[string]bool{
	"sudo": true, "doas": true, "su": true, "pkexec": true, "runuser": true, "setcap": true,
}

// setsSetuid reports a chmod that sets the setuid or setgid bit.
func setsSetuid(args []string) bool {
	_, operands := options(args)
	if len(operands) == 0 {
		return false
	}
	mode := unquote(operands[0])
	if len(mode) == 4 && (mode[0] == '4' || mode[0] == '6' || mode[0] == '2' || mode[0] == '7') {
		return allOctal(mode)
	}
	for _, clause := range strings.Split(mode, ",") {
		if i := strings.IndexAny(clause, "+="); i >= 0 && strings.Contains(clause[i:], "s") {
			return true
		}
	}
	return false
}

func allOctal(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '7' {
			return false
		}
	}
	return true
}

// fetchers download what a URL returns.
var fetchers = map[string]bool{"curl": true, "wget": true, "fetch": true, "aria2c": true, "http": true, "https": true, "xh": true}

// interpreters read a program from standard input or a file they are given.
var interpreters = map[string]bool{
	"python": true, "python2": true, "python3": true, "perl": true, "ruby": true, "node": true, "php": true,
	"lua": true, "deno": true, "bun": true, "source": true, ".": true,
}

func isInterpreter(name string) bool { return shells[name] || interpreters[name] }

// readsStdin reports an interpreter that would run what is piped to it: no
// file operand, a lone "-", or a shell's -s.
func readsStdin(name string, args []string) bool {
	if !isInterpreter(name) {
		return false
	}
	opts, operands := options(args)
	if shells[name] && shortFlag(opts, "c") {
		return false
	}
	if len(operands) == 0 {
		return true
	}
	if operands[0] == "-" {
		return true
	}
	return shells[name] && shortFlag(opts, "s")
}

// runsPayload reports eval, or a shell run with -c, whose script comes from a
// substitution.
func runsPayload(name string, args []string) bool {
	switch {
	case name == "eval":
		return true
	case shells[name]:
		opts, _ := options(args)
		return shortFlag(opts, "c")
	}
	return false
}

// gitHazards reads a git command line: the subcommand after git's own global
// options, and the arguments that make it destroy work.
func gitHazards(argv []string, hit map[string]bool) {
	g := withoutGlobals(argv, globalValueOptions["git"])
	if len(g) < 2 {
		return
	}
	sub, args := g[1], g[2:]
	if expands(sub) {
		hit[HazardOpaque] = true
		return
	}
	opts, operands := options(args)
	dynamic := false
	for _, a := range args {
		if expands(a) {
			dynamic = true
		}
	}
	switch sub {
	case "push":
		if shortFlag(opts, "fd") || hasLong(opts, "--force", "--force-with-lease", "--force-if-includes", "--delete", "--mirror", "--prune") {
			hit[HazardGitDestructive] = true
		}
		for _, o := range operands {
			if strings.HasPrefix(o, "+") || strings.HasPrefix(o, ":") {
				hit[HazardGitDestructive] = true
			}
		}
	case "reset":
		if dynamic {
			hit[HazardOpaque] = true
		}
		if hasLong(opts, "--hard", "--merge") {
			hit[HazardGitDestructive] = true
		}
	case "clean":
		if dynamic {
			hit[HazardOpaque] = true
		}
		if shortFlag(opts, "f") || hasLong(opts, "--force") {
			hit[HazardGitDestructive] = true
		}
	case "checkout", "restore", "switch":
		if shortFlag(opts, "f") || hasLong(opts, "--force", "--discard-changes") {
			hit[HazardGitDestructive] = true
		}
		if sub != "switch" {
			for _, o := range operands {
				switch unquote(o) {
				case ".", "*", ":/", ":/.":
					hit[HazardGitDestructive] = true
				}
			}
		}
	case "branch":
		if shortFlag(opts, "D") {
			hit[HazardGitDestructive] = true
		}
	case "stash":
		if len(operands) > 0 && (operands[0] == "drop" || operands[0] == "clear") {
			hit[HazardGitDestructive] = true
		}
	case "reflog":
		if len(operands) > 0 && (operands[0] == "expire" || operands[0] == "delete") {
			hit[HazardGitDestructive] = true
		}
	case "gc", "prune":
		if hasLong(opts, "--prune") || sub == "prune" {
			hit[HazardGitDestructive] = true
		}
	case "filter-branch", "filter-repo":
		hit[HazardGitDestructive] = true
	case "update-ref":
		if shortFlag(opts, "d") {
			hit[HazardGitDestructive] = true
		}
	}
}
