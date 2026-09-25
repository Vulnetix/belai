package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/proc"
)

// Vulnetix is the first-class Vulnetix CLI tool. Driving the CLI through Bash
// cost sessions minutes per call and mutated manifests: progress bars and
// banners filled the output, whole-repository reachability ran on every scan,
// `fix` without --dry-run edited go.mod, `fix` without --path failed on a
// repository with several manifests, and the 120 s Bash default killed scans
// that take one to three minutes. The tool fixes each of those in the argv it
// builds, so the model gets the useful answer on the first call:
//
//   - only an allowlist of read-only subcommands runs; `fix` always runs as
//     --dry-run and flags that apply changes are refused;
//   - --no-banner --no-progress --no-analytics and --disable-memory are
//     always added (memory.yaml keeps its single writer, the review's sca);
//   - scans and fix get an explicit --path (the working directory unless the
//     model names one), which also skips fix's multi-manifest prompt;
//   - reachability is off unless the model asks for it;
//   - the secrets stage scans the working tree, not git history (the CLI's
//     default walks up to 500 commits), unless the model asks for history;
//   - scans run one at a time, because they write the same .vulnetix/
//     artifacts, under a 15-minute limit;
//   - progress, spinner and ANSI noise is stripped before the output is
//     classified or shown.
//
// The output mixes advisory text from the Vulnetix database with repository
// paths and snippets, so the kind is KindRemote: read-only, classified.
type Vulnetix struct {
	Cwd      *Cwd
	Root     string
	Binary   string // empty means "vulnetix" on PATH
	Timeout  time.Duration
	MaxBytes int
}

// VulnetixTimeout is the default limit for one Vulnetix call.
const VulnetixTimeout = 15 * time.Minute

// scanFlags records which harness-added flags a scan-family subcommand
// accepts, from the CLI's command manifest (docs/command-manifest.json in the
// CLI repository). Adding a flag a subcommand lacks fails the call with
// "unknown flag", which is exactly the wasted round trip this tool exists to
// prevent.
type scanFlags struct{ path, memory, reachability bool }

// vulnetixScans are the scan-family subcommands. They write artifacts under
// .vulnetix/, so they are serialised.
var vulnetixScans = map[string]scanFlags{
	"scan":       {true, true, true},
	"sca":        {true, true, true},
	"sast":       {true, true, true},
	"secrets":    {true, true, true},
	"iac":        {true, true, true},
	"containers": {true, true, true},
	"fix":        {true, true, true},
	"malscan":    {true, true, false},
	"aibom":      {true, true, false},
	"cbom":       {true, true, false},
	"license":    {true, true, false},
	"sbom":       {false, false, false},
}

// vulnetixReadOnly are the other allowed subcommand paths. vdb lookups are
// allowed except the ones that write files or clear the cache.
var vulnetixReadOnly = map[string]bool{
	"vdb": true, "env": true, "version": true, "auth status": true,
}

// vulnetixDeniedVDB are vdb subcommands that write to disk.
var vulnetixDeniedVDB = map[string]bool{
	"cache": true, "download": true, "poc": true, "fetch": true,
}

// vulnetixDeniedFlags apply changes or upload. fix's changes are made by the
// model with Edit from the dry-run plan, never by the CLI.
var vulnetixDeniedFlags = map[string]string{
	"--yes":         "apply fixes with Edit from the --dry-run plan instead",
	"--sca-autofix": "apply fixes with Edit from `fix --dry-run` instead",
	"--jail":        "jail uploads and gates on org policy; run it from /vulnetix",
}

// vulnetixScanLock serialises scans: two concurrent scans write the same
// .vulnetix/ artifacts and double the CPU cost of both.
var vulnetixScanLock sync.Mutex

// Definition returns the tool metadata. The description is the usage guide
// the model otherwise lacks.
func (v *Vulnetix) Definition() Definition {
	return Definition{
		Name: "Vulnetix",
		Description: "Run the Vulnetix CLI: SCA and code scans, dependency fix plans, and vulnerability database lookups. " +
			"Pass the arguments that follow `vulnetix`, for example `sca`, `sca --path web --depth 1`, `fix --dry-run --manifest go.mod`, `vdb vuln CVE-2021-44228`, `sca --help`. " +
			"Allowed: scan, sca, sast, secrets, iac, containers, malscan, sbom, aibom, cbom, license, fix (always --dry-run), vdb lookups, env, version, auth status, and --help on any of them. " +
			"The harness adds --no-banner --no-progress --no-analytics --disable-memory, passes --path (the working directory unless you give one), and turns reachability off unless you pass --reachability direct|transitive|both, because it is the slowest stage, and scans secrets in the working tree only (--ignore-git) unless you pass --git-history, because walking commit history is slow. " +
			"A scan takes one to three minutes on a large repository: scope it with --path DIR --depth 1, run it once, and use -o json-cyclonedx or -o json-sarif when you need structured output rather than re-running with a different view. " +
			"Before scanning, look in .vulnetix/ for the last /vulnetix review's results (sbom.cdx.json, sast.sarif, secrets.sarif, …). " +
			"`fix` only plans: apply the manifest edits it proposes with Edit, then re-scan that directory. " +
			"Exit status 1 means a gate found something (a vulnerability, exploit, end-of-life component or malware), not that the command failed. " +
			"Do not run vulnetix through Bash.",
		Properties: map[string]Property{
			"command": stringProp("The vulnetix arguments, e.g. \"sca --path web\" or \"fix --dry-run --manifest go.mod\""),
		},
		Required: []string{"command"},
	}
}

// Kind is KindRemote: read-only, and classified before promotion.
func (v *Vulnetix) Kind() Kind { return KindRemote }

// Subject is the command, for permission rules such as Vulnetix(sca*).
func (v *Vulnetix) Subject(args map[string]any) string {
	s, _ := argString(args, "command")
	return strings.TrimSpace(s)
}

// Execute validates, shapes and runs one call.
func (v *Vulnetix) Execute(ctx context.Context, args map[string]any) (Result, error) {
	raw, _ := argString(args, "command")
	argv, scan, note, err := v.buildArgv(raw)
	if err != nil {
		return Result{}, err
	}
	if scan {
		vulnetixScanLock.Lock()
		defer vulnetixScanLock.Unlock()
	}
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = VulnetixTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	bin := v.Binary
	if bin == "" {
		bin = "vulnetix"
	}
	ec := exec.CommandContext(ctx, bin, argv...)
	ec.Dir = baseDir(v.Root, v.Cwd)
	ec.Env = append(proc.ScrubbedEnv(), calltrace.Env(ctx)...)
	ec.WaitDelay = 2 * time.Second
	proc.SetProcessGroup(ec)
	maxBytes := v.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 256 * 1024 // read raw, then cleaned and capped below
	}
	tw := proc.NewLineTee(maxBytes, nil)
	ec.Stdout, ec.Stderr = tw, tw
	start := time.Now()
	if err := ec.Start(); err != nil {
		return Result{}, err
	}
	werr := ec.Wait()
	tw.Flush()

	out := CleanVulnetixOutput(tw.Content())
	var footer []string
	if note != "" {
		footer = append(footer, note)
	}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		footer = append(footer, fmt.Sprintf("timed out after %s; scope the scan with --path DIR --depth 1", timeout))
	case werr != nil:
		code := exitCode(werr)
		if code == 1 && scan {
			footer = append(footer, "exit status 1: a gate found something")
		} else {
			footer = append(footer, fmt.Sprintf("exit status %d", code))
		}
	default:
		footer = append(footer, "exit status 0")
	}
	footer = append(footer, fmt.Sprintf("ran `vulnetix %s` in %s", strings.Join(argv, " "), time.Since(start).Round(time.Second)))
	return Result{Kind: KindRemote, Content: out + "\n\n" + strings.Join(footer, "\n")}, nil
}

// buildArgv turns the model's arguments into the argv that runs. It reports
// whether the call is a scan (serialised) and a note about anything the
// harness changed, so the model is told rather than surprised.
func (v *Vulnetix) buildArgv(raw string) (argv []string, scan bool, note string, err error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "vulnetix "))
	if raw == "" {
		return nil, false, "", fmt.Errorf("missing command argument")
	}
	if strings.ContainsAny(raw, ShellMetacharacters+`"'\`) {
		return nil, false, "", fmt.Errorf("command contains shell metacharacters or quotes: pass plain vulnetix arguments, not a shell line")
	}
	fields := strings.Fields(raw)
	var sub []string
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			break
		}
		sub = append(sub, f)
	}
	if len(sub) == 0 {
		return nil, false, "", fmt.Errorf("name a subcommand first, e.g. `sca` or `vdb vuln CVE-…`")
	}
	help := hasFlag(fields, "--help") || hasFlag(fields, "-h") || sub[0] == "help"
	first := sub[0]
	switch {
	case help:
		// --help on anything is harmless and instant.
	case isScanCommand(first):
		scan = true
	case first == "vdb":
		for _, s := range sub[1:] {
			if vulnetixDeniedVDB[s] {
				return nil, false, "", fmt.Errorf("vdb %s writes to disk and is not allowed here", s)
			}
		}
	case vulnetixReadOnly[strings.Join(sub, " ")] || vulnetixReadOnly[first]:
	default:
		return nil, false, "", fmt.Errorf("vulnetix %s is not allowed here; allowed: scan, sca, sast, secrets, iac, containers, malscan, sbom, aibom, cbom, license, fix --dry-run, vdb lookups, env, version, auth status, --help", first)
	}

	var notes []string
	out := []string{"--no-banner", "--no-progress", "--no-analytics"}
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		name, _, _ := strings.Cut(f, "=")
		if why, denied := vulnetixDeniedFlags[name]; denied && !help {
			return nil, false, "", fmt.Errorf("%s is not allowed: %s", name, why)
		}
		if name == "--path" || name == "-o" || name == "--output" || name == "--output-file" {
			val, next, ok := flagValue(fields, i)
			if !ok {
				return nil, false, "", fmt.Errorf("%s needs a value", name)
			}
			i = next
			if name != "--path" && (val == "json-cyclonedx" || val == "json-sarif") {
				out = append(out, name, val)
				continue
			}
			abs, perr := v.confine(val)
			if perr != nil {
				return nil, false, "", fmt.Errorf("%s %s: %w", name, val, perr)
			}
			out = append(out, name, abs)
			continue
		}
		out = append(out, f)
	}
	if help {
		return out, false, "", nil
	}
	flags, isScan := vulnetixScans[first]
	// Every non-scan subcommand accepts --disable-memory (it is a root flag
	// on vdb, env and auth); a scan only when the manifest says so.
	if !hasFlag(fields, "--disable-memory") && (!isScan || flags.memory) {
		out = append(out, "--disable-memory")
	}
	if scan {
		if flags.path && !hasFlag(fields, "--path") {
			out = append(out, "--path", baseDir(v.Root, v.Cwd))
		}
		if flags.reachability && !hasFlag(fields, "--reachability") {
			out = append(out, "--reachability", "off")
			notes = append(notes, "reachability was off (pass --reachability direct|transitive|both to include it)")
		}
		// The secrets stage walks git history by default, which is most of its
		// run time on a long-lived repository. The working tree is the
		// default; an explicit --ignore-git or any --git-history* flag is the
		// model's own choice and is left alone.
		secretsStage := first == "secrets" || (first == "scan" && hasFlag(fields, "--evaluate-secrets"))
		if secretsStage && !hasFlag(fields, "--ignore-git") && !hasFlagPrefix(fields, "--git-history") {
			out = append(out, "--ignore-git")
			notes = append(notes, "secrets scanned the working tree only (pass --git-history to include commit history)")
		}
		if first == "fix" && !hasFlag(fields, "--dry-run") {
			out = append(out, "--dry-run")
			notes = append(notes, "fix ran as --dry-run: apply the planned edits with Edit")
		}
	}
	return out, scan, strings.Join(notes, "; "), nil
}

// confine resolves a path argument against the working directory and
// refuses one outside the session root.
func (v *Vulnetix) confine(p string) (string, error) {
	base := baseDir(v.Root, v.Cwd)
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(base, abs)
	}
	abs = filepath.Clean(abs)
	root := filepath.Clean(v.Root)
	if root != "" && abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("outside the working tree")
	}
	return abs, nil
}

func hasFlag(fields []string, name string) bool {
	for _, f := range fields {
		if f == name || strings.HasPrefix(f, name+"=") {
			return true
		}
	}
	return false
}

// hasFlagPrefix reports whether any field starts with prefix, e.g.
// --git-history, --git-history=false or --git-history-max-commits.
func hasFlagPrefix(fields []string, prefix string) bool {
	for _, f := range fields {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

// flagValue returns the value of the flag at i (`--flag v` or `--flag=v`)
// and the index of the last field it consumed.
func flagValue(fields []string, i int) (string, int, bool) {
	if _, val, ok := strings.Cut(fields[i], "="); ok {
		return val, i, val != ""
	}
	if i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "-") {
		return fields[i+1], i + 1, true
	}
	return "", i, false
}

var (
	ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	// progressLine matches bar and spinner lines: block glyphs, braille
	// spinners, or a percentage counter such as "3/7 (42%)".
	progressLine = regexp.MustCompile(`[░▒▓█⠁-⣿]|\b\d+/\d+ \(\d+%\)`)
)

// maxVulnetixOutput bounds the cleaned output handed to the classifier and
// the model: head and tail are kept, since the summary and the findings
// table sit at the ends.
const maxVulnetixOutput = 48 * 1024

// CleanVulnetixOutput strips ANSI codes, carriage-return redraws, progress
// bars and spinners, collapses blank runs, and keeps the head and tail of an
// over-long report.
func CleanVulnetixOutput(s string) string {
	s = ansiEscape.ReplaceAllString(s, "")
	var kept []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:] // a redrawn line keeps only its final state
		}
		line = strings.TrimRight(line, " \t")
		if progressLine.MatchString(line) {
			continue
		}
		if line == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		kept = append(kept, line)
	}
	out := strings.TrimSpace(strings.Join(kept, "\n"))
	if len(out) <= maxVulnetixOutput {
		return out
	}
	head, tail := out[:maxVulnetixOutput/4], out[len(out)-maxVulnetixOutput*3/4:]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i]
	}
	if i := strings.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	return head + fmt.Sprintf("\n… %d bytes omitted; use -o json-cyclonedx or -o json-sarif for structured output …\n", len(out)-len(head)-len(tail)) + tail
}

// VulnetixInCommand reports whether a shell command runs the vulnetix binary
// in command position in any of its segments. Arguments that merely mention
// the word (grep vulnetix, ls ~/Vulnetix) do not count.
func VulnetixInCommand(cmd string) bool {
	seps := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n", "&", "\n", "$(", "\n", "`", "\n", "(", "\n")
	for _, seg := range strings.Split(seps.Replace(cmd), "\n") {
		fields := strings.Fields(seg)
		for len(fields) > 0 {
			f := fields[0]
			switch {
			case strings.Contains(f, "=") && !strings.HasPrefix(f, "-"):
				fields = fields[1:] // VAR=value prefix
				continue
			case f == "env" || f == "sudo" || f == "command" || f == "exec" || f == "time" || f == "nice" || f == "nohup":
				fields = fields[1:]
				continue
			case f == "timeout" && len(fields) > 1:
				fields = fields[2:]
				continue
			}
			break
		}
		if len(fields) > 0 && filepath.Base(fields[0]) == "vulnetix" {
			return true
		}
	}
	return false
}

func isScanCommand(sub string) bool {
	_, ok := vulnetixScans[sub]
	return ok
}
