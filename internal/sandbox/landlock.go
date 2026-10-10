package sandbox

// Landlock, the kernel's own sandbox (Linux 5.13+), stands in where bubblewrap
// cannot run: no user namespaces (Ubuntu's AppArmor default, a Docker
// container without them), no setuid. It restricts a thread and everything it
// execs, so the command is started through a hidden helper, `belai
// __landlock-exec -- argv`, that reads the policy from its environment,
// restricts itself and execs the command.
//
// Two facts shape the mapping. Within a ruleset the rights that apply to a
// file are the union of the rules found on the walk from it up to /: there
// are no deny rules, and a rule on a descendant can only add. So "readable /
// with a hidden directory inside it" and "a writable directory whose entries
// are read-only" cannot be laid over each other as bubblewrap's mounts are;
// they are carved: the ancestors of a hole get only what the hole allows (plus
// listing), and their other children get the full rights, one rule each.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// LandlockCommand is the hidden subcommand that applies a policy and execs.
const LandlockCommand = "__landlock-exec"

// landlockPolicyEnv carries the policy to the helper, which removes it before
// the command starts.
const landlockPolicyEnv = "BELAI_LANDLOCK_POLICY"

// landlockNetABI is the first Landlock ABI that can restrict TCP (Linux 6.7).
const landlockNetABI = 4

// Access bits as uapi/linux/landlock.h defines them. They are local so the
// plan and its tests build on every platform; landlock_linux_test.go holds
// them to x/sys.
const (
	llExecute    uint64 = 1 << 0
	llWriteFile  uint64 = 1 << 1
	llReadFile   uint64 = 1 << 2
	llReadDir    uint64 = 1 << 3
	llRemoveDir  uint64 = 1 << 4
	llRemoveFile uint64 = 1 << 5
	llMakeChar   uint64 = 1 << 6
	llMakeDir    uint64 = 1 << 7
	llMakeReg    uint64 = 1 << 8
	llMakeSock   uint64 = 1 << 9
	llMakeFifo   uint64 = 1 << 10
	llMakeBlock  uint64 = 1 << 11
	llMakeSym    uint64 = 1 << 12
	llRefer      uint64 = 1 << 13 // ABI 2
	llTruncate   uint64 = 1 << 14 // ABI 3
	llIoctlDev   uint64 = 1 << 15 // ABI 5

	llNetBindTCP    uint64 = 1 << 0 // ABI 4
	llNetConnectTCP uint64 = 1 << 1

	// llList is the one right an ancestor of a hidden path keeps.
	llList = llReadDir
	// llRead is what a read-only path has.
	llRead = llReadFile | llReadDir | llExecute
	// llFileMask is every right that applies to a file: the rest are for
	// directories and the kernel refuses them on anything else.
	llFileMask = llExecute | llWriteFile | llReadFile | llTruncate | llIoctlDev
)

// llHandledFS is every filesystem right the ABI knows; a ruleset handles them
// all, so anything not granted is denied.
func llHandledFS(abi int) uint64 {
	bits := llExecute | llWriteFile | llReadFile | llReadDir | llRemoveDir | llRemoveFile |
		llMakeChar | llMakeDir | llMakeReg | llMakeSock | llMakeFifo | llMakeBlock | llMakeSym
	if abi >= 2 {
		bits |= llRefer
	}
	if abi >= 3 {
		bits |= llTruncate
	}
	if abi >= 5 {
		bits |= llIoctlDev
	}
	return bits
}

// llWrite is what a writable path has besides reading, at this ABI.
func llWrite(abi int) uint64 {
	return llHandledFS(abi) &^ llRead
}

// llRW is a writable path's rights.
func llRW(abi int) uint64 { return llRead | llWrite(abi) }

// landlockPolicy is what the helper is told: the parts of Policy that name
// paths, and the network switch. Visible needs nothing (the root is readable),
// and Env has already been added to the command.
type landlockPolicy struct {
	Writable    []string `json:"writable"`
	Hidden      []string `json:"hidden"`
	Mounts      []Mount  `json:"mounts,omitempty"`
	DenyNetwork bool     `json:"deny_network"`
}

func landlockPolicyFrom(p Policy) landlockPolicy {
	pol := landlockPolicy{Writable: uniq(p.Writable), Hidden: uniq(p.Hidden), DenyNetwork: p.DenyNetwork}
	for _, m := range p.Mounts {
		pol.Mounts = append(pol.Mounts, Mount{Path: filepath.Clean(m.Path), Writable: m.Writable})
	}
	return pol
}

// errLandlockPolicy says the ordered mounts ask for something Landlock cannot
// express. It refuses in every mode: widening is never the answer.
var errLandlockPolicy = errors.New("the sandbox policy lays a read-only path over a writable one beneath it, which Landlock cannot express, so the command was not run")

// landlockPolicyProblem checks the ordered mounts. A read-only mount beneath a
// writable one is a hole the carve expresses; a read-only mount over an
// earlier writable path would have to take that path's rights away, which no
// rule can.
func landlockPolicyProblem(pol landlockPolicy) error {
	writable := append([]string{}, pol.Writable...)
	for _, m := range pol.Mounts {
		if m.Writable {
			writable = append(writable, m.Path)
			continue
		}
		for _, w := range writable {
			if w == m.Path || beneath(w, m.Path) {
				return fmt.Errorf("%w (%s over %s)", errLandlockPolicy, m.Path, w)
			}
		}
	}
	return nil
}

// beneath reports whether p is strictly inside root.
func beneath(p, root string) bool {
	if root == "/" {
		return p != "/"
	}
	return strings.HasPrefix(p, root+"/")
}

// llEntry is one directory entry, as the helper sees it. A symlink is skipped:
// the walk resolves it to its target, whose own hierarchy's rules apply.
type llEntry struct {
	Name    string
	Dir     bool
	Symlink bool
}

// llRule is one Landlock rule: a path and the rights allowed beneath it.
type llRule struct {
	Path   string
	Access uint64
}

// llPlan is the ruleset to apply: rules by path, whether TCP is cut off, and
// what the carve could not keep (for /sandbox and tests).
type llPlan struct {
	Rules []llRule
	Net   bool
	Notes []string
}

// llSpec is one path the policy names with the rights it should have.
type llSpec struct {
	path   string
	rights uint64
	// file marks a device node: a file rule, never carved.
	file bool
}

// devNodes is what bubblewrap's --dev provides: the nodes a command may use,
// never the host's whole /dev (no audio, no disks).
var devNodes = []string{"null", "zero", "full", "random", "urandom", "tty", "ptmx"}

// devDirs are the writable directories under /dev: terminals and shared memory.
var devDirs = []string{"pts", "shm"}

// landlockPlan turns a policy into rules for the kernel's ABI. list reads a
// directory: its entries and whether the path is a directory at all (a file
// or a missing path is not; a missing path's rule is skipped when applied,
// like bubblewrap's --bind-try).
func landlockPlan(pol landlockPolicy, abi int, list func(dir string) ([]llEntry, bool)) (llPlan, error) {
	if err := landlockPolicyProblem(pol); err != nil {
		return llPlan{}, err
	}
	rw, read := llRW(abi), llRead
	// The specs, last one on a path winning, hidden winning over all.
	byPath := map[string]llSpec{}
	put := func(s llSpec) { byPath[s.path] = s }
	put(llSpec{path: "/", rights: read})
	put(llSpec{path: "/dev", rights: llList})
	for _, n := range devNodes {
		put(llSpec{path: "/dev/" + n, rights: rw, file: true})
	}
	for _, d := range devDirs {
		put(llSpec{path: "/dev/" + d, rights: rw})
	}
	put(llSpec{path: "/tmp", rights: rw})
	for _, w := range pol.Writable {
		put(llSpec{path: w, rights: rw})
	}
	for _, m := range pol.Mounts {
		if m.Writable {
			put(llSpec{path: m.Path, rights: rw})
		} else {
			put(llSpec{path: m.Path, rights: read})
		}
	}
	for _, h := range pol.Hidden {
		put(llSpec{path: h, rights: 0})
	}
	// Nothing beneath a hidden path gets a rule of its own: hidden wins.
	for p := range byPath {
		for _, h := range pol.Hidden {
			if beneath(p, h) {
				delete(byPath, p)
			}
		}
	}
	specs := make([]llSpec, 0, len(byPath))
	for _, s := range byPath {
		specs = append(specs, s)
	}
	sort.Slice(specs, func(i, j int) bool {
		di, dj := strings.Count(specs[i].path, "/"), strings.Count(specs[j].path, "/")
		if di != dj {
			return di < dj
		}
		return specs[i].path < specs[j].path
	})

	rules := map[string]uint64{}
	emit := func(path string, rights uint64, dir bool) {
		if !dir {
			rights &= llFileMask
		}
		if rights != 0 {
			rules[path] |= rights
		}
	}
	// holesBeneath are the specs inside dir that allow less than rights.
	holesBeneath := func(dir string, rights uint64) []llSpec {
		var out []llSpec
		for _, s := range specs {
			if beneath(s.path, dir) && s.rights&rights != rights {
				out = append(out, s)
			}
		}
		return out
	}
	var notes []string
	var carve func(dir string, rights uint64, holes []llSpec)
	carve = func(dir string, rights uint64, holes []llSpec) {
		keep := ^uint64(0)
		for _, h := range holes {
			keep &= h.rights
		}
		entries, isDir := list(dir)
		if !isDir {
			emit(dir, rights, false)
			return
		}
		emit(dir, llList|(keep&rights), true)
		for _, e := range entries {
			if e.Symlink {
				continue
			}
			child := filepath.Join(dir, e.Name)
			if _, isHole := byPath[child]; isHole && byPath[child].rights&rights != rights {
				// Its own spec decides what it gets.
				continue
			}
			if hs := holesBeneath(child, rights); len(hs) > 0 {
				carve(child, rights, hs)
				continue
			}
			emit(child, rights, e.Dir)
		}
	}
	for _, s := range specs {
		if s.rights == 0 {
			continue
		}
		if s.file {
			emit(s.path, s.rights, false)
			continue
		}
		holes := holesBeneath(s.path, s.rights)
		if len(holes) == 0 {
			_, isDir := list(s.path)
			emit(s.path, s.rights, isDir)
			continue
		}
		if s.rights&llWrite(abi) != 0 {
			notes = append(notes, s.path+": its top is read-only under Landlock (a writable directory holding read-only or hidden paths cannot be expressed); the paths beneath keep their own rights")
		}
		carve(s.path, s.rights, holes)
	}
	plan := llPlan{Net: pol.DenyNetwork && abi >= landlockNetABI, Notes: notes}
	for p, r := range rules {
		plan.Rules = append(plan.Rules, llRule{Path: p, Access: r})
	}
	sort.Slice(plan.Rules, func(i, j int) bool { return plan.Rules[i].Path < plan.Rules[j].Path })
	return plan, nil
}

// osList reads a directory for the plan.
func osList(dir string) ([]llEntry, bool) {
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() {
		return nil, false
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, true
	}
	out := make([]llEntry, 0, len(des))
	for _, d := range des {
		out = append(out, llEntry{Name: d.Name(), Dir: d.IsDir(), Symlink: d.Type()&os.ModeSymlink != 0})
	}
	return out, true
}

// HelperMain runs the Landlock helper when this process was started as one,
// and never returns then. A main, and the TestMain of a package whose tests
// run sandboxed commands (the test binary is then the helper), call it first.
func HelperMain() {
	if len(os.Args) > 1 && os.Args[1] == LandlockCommand {
		os.Exit(LandlockExecMain(os.Args[2:], os.Stdout, os.Stderr))
	}
}

// LandlockExecMain is the helper: `-probe` prints the kernel's ABI, and
// `-- command args` applies the policy from the environment and execs the
// command. Exit 3: no Landlock here. Exit 125: the policy could not be applied,
// and nothing ran. Exit 127: the command could not be started.
func LandlockExecMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "-probe" {
		abi, err := landlockABI()
		if err != nil {
			fmt.Fprintf(stderr, "landlock: %v\n", err)
			return 3
		}
		fmt.Fprintf(stdout, "abi=%d\n", abi)
		return 0
	}
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintf(stderr, "usage: belai %s -- command [args]\n", LandlockCommand)
		return 125
	}
	argv := args[1:]
	// The restriction lands on this thread; the exec must come from it.
	runtime.LockOSThread()
	raw, ok := os.LookupEnv(landlockPolicyEnv)
	_ = os.Unsetenv(landlockPolicyEnv)
	if !ok || raw == "" {
		fmt.Fprintf(stderr, "belai %s: no policy in %s; nothing was run\n", LandlockCommand, landlockPolicyEnv)
		return 125
	}
	var pol landlockPolicy
	if err := json.Unmarshal([]byte(raw), &pol); err != nil {
		fmt.Fprintf(stderr, "belai %s: the policy does not parse: %v; nothing was run\n", LandlockCommand, err)
		return 125
	}
	abi, err := landlockABI()
	if err != nil {
		fmt.Fprintf(stderr, "belai %s: landlock: %v; nothing was run\n", LandlockCommand, err)
		return 125
	}
	plan, err := landlockPlan(pol, abi, osList)
	if err != nil {
		fmt.Fprintf(stderr, "belai %s: %v\n", LandlockCommand, err)
		return 125
	}
	if err := applyLandlock(plan, abi); err != nil {
		fmt.Fprintf(stderr, "belai %s: %v; nothing was run\n", LandlockCommand, err)
		return 125
	}
	if err := dieWithParent(); err != nil {
		fmt.Fprintf(stderr, "belai %s: %v\n", LandlockCommand, err)
		return 125
	}
	if err := execProcess(argv); err != nil {
		fmt.Fprintf(stderr, "belai %s: %v\n", LandlockCommand, err)
		return 127
	}
	return 127
}
