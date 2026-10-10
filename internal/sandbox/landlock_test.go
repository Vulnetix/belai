package sandbox

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTree is a directory tree for the plan: a path maps to its entries.
// Anything not listed is a file.
type fakeTree map[string][]llEntry

func (t fakeTree) list(dir string) ([]llEntry, bool) {
	es, ok := t[dir]
	return es, ok
}

func dir(name string) llEntry  { return llEntry{Name: name, Dir: true} }
func file(name string) llEntry { return llEntry{Name: name} }
func link(name string) llEntry { return llEntry{Name: name, Symlink: true} }

// homeTree is a small machine: / with the usual entries, a home with a
// hidden Belai state directory, and a workspace.
func homeTree() fakeTree {
	return fakeTree{
		"/":                       {dir("dev"), dir("etc"), dir("home"), dir("proc"), dir("tmp"), dir("usr"), link("bin"), file("vmlinuz")},
		"/dev":                    {file("null"), file("tty"), dir("pts"), dir("shm"), file("sda")},
		"/dev/pts":                {},
		"/dev/shm":                {},
		"/etc":                    {file("passwd")},
		"/home":                   {dir("x")},
		"/home/x":                 {dir(".vulnetix"), dir("work"), file(".bashrc"), link("go")},
		"/home/x/.vulnetix":       {dir("belai"), file("other")},
		"/home/x/.vulnetix/belai": {file("credentials")},
		"/home/x/work":            {file("main.go")},
		"/proc":                   {},
		"/tmp":                    {},
		"/usr":                    {dir("bin")},
		"/usr/bin":                {file("sh")},
	}
}

func rulesByPath(p llPlan) map[string]uint64 {
	out := map[string]uint64{}
	for _, r := range p.Rules {
		out[r.Path] = r.Access
	}
	return out
}

func TestLandlockPlanCarvesTheHiddenDirectoryOutOfTheReadableRoot(t *testing.T) {
	pol := landlockPolicy{Writable: []string{"/home/x/work"}, Hidden: []string{"/home/x/.vulnetix/belai"}}
	plan, err := landlockPlan(pol, 5, homeTree().list)
	if err != nil {
		t.Fatal(err)
	}
	r := rulesByPath(plan)
	rw := llRW(5)
	for path, want := range map[string]uint64{
		"/":                       llList,
		"/home":                   llList,
		"/home/x":                 llList,
		"/home/x/.vulnetix":       llList,
		"/home/x/.vulnetix/other": llRead & llFileMask,
		"/home/x/.bashrc":         llRead & llFileMask,
		"/home/x/work":            rw,
		"/etc":                    llRead,
		"/usr":                    llRead,
		"/proc":                   llRead,
		"/vmlinuz":                llRead & llFileMask,
		"/tmp":                    rw,
		"/dev":                    llList,
		"/dev/null":               rw & llFileMask,
		"/dev/tty":                rw & llFileMask,
		"/dev/pts":                rw,
		"/dev/shm":                rw,
	} {
		if got := r[path]; got != want {
			t.Errorf("%s: rights %#x, want %#x", path, got, want)
		}
	}
	for _, none := range []string{"/home/x/.vulnetix/belai", "/home/x/.vulnetix/belai/credentials", "/bin", "/home/x/go", "/dev/sda"} {
		if _, ok := r[none]; ok {
			t.Errorf("%s has a rule, which it must not", none)
		}
	}
	if plan.Net || len(plan.Notes) != 0 {
		t.Fatalf("net %v notes %v", plan.Net, plan.Notes)
	}
}

func TestLandlockPlanCarvesAWritableRootThatHoldsTheHiddenDirectory(t *testing.T) {
	pol := landlockPolicy{Writable: []string{"/home/x"}, Hidden: []string{"/home/x/.vulnetix/belai"}}
	plan, err := landlockPlan(pol, 5, homeTree().list)
	if err != nil {
		t.Fatal(err)
	}
	r := rulesByPath(plan)
	rw := llRW(5)
	if r["/home/x"] != llList || r["/home/x/work"] != rw || r["/home/x/.vulnetix"] != llList || r["/home/x/.vulnetix/other"] != rw&llFileMask {
		t.Fatalf("rules: %#x %#x %#x %#x", r["/home/x"], r["/home/x/work"], r["/home/x/.vulnetix"], r["/home/x/.vulnetix/other"])
	}
	if _, ok := r["/home/x/.vulnetix/belai"]; ok {
		t.Fatal("the hidden directory has a rule")
	}
	if len(plan.Notes) != 1 || !strings.HasPrefix(plan.Notes[0], "/home/x: its top is read-only under Landlock") {
		t.Fatalf("notes: %v", plan.Notes)
	}
}

// The fleet's git common dir: writable, every entry read-only, and this item's
// ref directory, its reflogs and its worktree admin dir writable again.
func TestLandlockPlanKeepsAWorktreeCommonDirReadOnlyWithItsOwnPathsWritable(t *testing.T) {
	common := "/repo/.git"
	tree := homeTree()
	tree["/"] = append(tree["/"], dir("repo"))
	tree["/repo"] = []llEntry{dir(".git"), dir("src"), dir("wt")}
	tree["/repo/wt"] = []llEntry{file("a")}
	tree[common] = []llEntry{file("HEAD"), file("config"), dir("hooks"), dir("objects"), dir("refs"), dir("logs"), dir("worktrees"), file("packed-refs")}
	tree[common+"/refs"] = []llEntry{dir("heads")}
	tree[common+"/refs/heads"] = []llEntry{dir("main"), dir("belai")}
	tree[common+"/refs/heads/belai"] = []llEntry{dir("K-x"), dir("K-y")}
	tree[common+"/refs/heads/belai/K-x"] = []llEntry{}
	tree[common+"/logs"] = []llEntry{dir("refs")}
	tree[common+"/logs/refs"] = []llEntry{dir("heads")}
	tree[common+"/logs/refs/heads"] = []llEntry{dir("belai")}
	tree[common+"/logs/refs/heads/belai"] = []llEntry{dir("K-x")}
	tree[common+"/logs/refs/heads/belai/K-x"] = []llEntry{}
	tree[common+"/worktrees"] = []llEntry{dir("wt")}
	tree[common+"/worktrees/wt"] = []llEntry{file("HEAD"), file("index")}
	tree[common+"/hooks"] = []llEntry{}
	tree[common+"/objects"] = []llEntry{}
	mounts := []Mount{{Path: common, Writable: true}}
	for _, e := range tree[common] {
		mounts = append(mounts, Mount{Path: filepath.Join(common, e.Name)})
	}
	mounts = append(mounts,
		Mount{Path: common + "/refs/heads/belai/K-x", Writable: true},
		Mount{Path: common + "/logs/refs/heads/belai/K-x", Writable: true},
		Mount{Path: common + "/worktrees/wt", Writable: true},
	)
	pol := landlockPolicy{Writable: []string{"/repo/wt"}, Mounts: mounts}
	plan, err := landlockPlan(pol, 5, tree.list)
	if err != nil {
		t.Fatal(err)
	}
	r := rulesByPath(plan)
	rw := llRW(5)
	for path, want := range map[string]uint64{
		common:                                llRead,
		common + "/HEAD":                      llRead & llFileMask,
		common + "/config":                    llRead & llFileMask,
		common + "/hooks":                     llRead,
		common + "/objects":                   llRead,
		common + "/refs":                      llRead,
		common + "/refs/heads/belai/K-x":      rw,
		common + "/logs":                      llRead,
		common + "/logs/refs/heads/belai/K-x": rw,
		common + "/worktrees":                 llRead,
		common + "/worktrees/wt":              rw,
		"/repo/wt":                            rw,
	} {
		if got := r[path]; got != want {
			t.Errorf("%s: rights %#x, want %#x", path, got, want)
		}
	}
	if _, ok := r[common+"/refs/heads/belai/K-y"]; ok {
		t.Error("another item's ref directory has a rule of its own")
	}
	if len(plan.Notes) != 1 || !strings.HasPrefix(plan.Notes[0], common+": its top is read-only under Landlock") {
		t.Fatalf("notes: %v", plan.Notes)
	}
}

func TestLandlockPlanRefusesAReadOnlyMountOverAnEarlierWritablePath(t *testing.T) {
	pol := landlockPolicy{Writable: []string{"/home/x/work"}, Mounts: []Mount{{Path: "/home/x"}}}
	if _, err := landlockPlan(pol, 5, homeTree().list); !errors.Is(err, errLandlockPolicy) {
		t.Fatalf("err = %v", err)
	}
	if err := landlockPolicyProblem(landlockPolicy{Mounts: []Mount{{Path: "/a", Writable: true}, {Path: "/a"}}}); !errors.Is(err, errLandlockPolicy) {
		t.Fatalf("the same path read-only after writable: %v", err)
	}
	if err := landlockPolicyProblem(landlockPolicy{Mounts: []Mount{{Path: "/a", Writable: true}, {Path: "/a/b"}, {Path: "/a/b/c", Writable: true}}}); err != nil {
		t.Fatalf("a read-only path inside a writable one, and a writable one inside that: %v", err)
	}
}

func TestLandlockPlanMasksRightsToTheABI(t *testing.T) {
	tree := homeTree()
	pol := landlockPolicy{Writable: []string{"/home/x/work"}, DenyNetwork: true}
	for _, c := range []struct {
		abi  int
		has  uint64
		lack uint64
		net  bool
	}{
		{1, llMakeReg | llRemoveFile, llRefer | llTruncate | llIoctlDev, false},
		{2, llRefer, llTruncate | llIoctlDev, false},
		{3, llRefer | llTruncate, llIoctlDev, false},
		{4, llTruncate, llIoctlDev, true},
		{5, llRefer | llTruncate | llIoctlDev, 0, true},
	} {
		plan, err := landlockPlan(pol, c.abi, tree.list)
		if err != nil {
			t.Fatal(err)
		}
		r := rulesByPath(plan)
		w := r["/home/x/work"]
		if w&c.has != c.has || w&c.lack != 0 || plan.Net != c.net {
			t.Errorf("ABI %d: work %#x net %v", c.abi, w, plan.Net)
		}
		if tty := r["/dev/tty"]; (c.abi >= 5) != (tty&llIoctlDev != 0) || tty&(llMakeReg|llReadDir) != 0 {
			t.Errorf("ABI %d: /dev/tty %#x", c.abi, tty)
		}
	}
}

func TestLandlockPlanMergesRulesOnOnePath(t *testing.T) {
	pol := landlockPolicy{Writable: []string{"/tmp", "/home/x/work", "/home/x/work"}, Mounts: []Mount{{Path: "/home/x/work"}}}
	plan, err := landlockPlan(pol, 5, homeTree().list)
	if err == nil {
		t.Fatal("a read-only mount on a writable path must be refused")
	}
	pol.Mounts = nil
	plan, err = landlockPlan(pol, 5, homeTree().list)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range plan.Rules {
		seen[r.Path]++
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("%s has %d rules", p, n)
		}
	}
	if rulesByPath(plan)["/tmp"] != llRW(5) {
		t.Fatal("/tmp lost its rights")
	}
}

func TestLandlockPolicyRoundTripsThroughJSON(t *testing.T) {
	p := Policy{Writable: []string{"/w", "/w"}, Visible: []string{"/v"}, Hidden: []string{"/h"}, Mounts: []Mount{{Path: "/m/", Writable: true}}, DenyNetwork: true, Env: []string{"A=b"}}
	pol := landlockPolicyFrom(p)
	js, err := json.Marshal(pol)
	if err != nil {
		t.Fatal(err)
	}
	var back landlockPolicy
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Writable) != 1 || back.Writable[0] != "/w" || back.Hidden[0] != "/h" || back.Mounts[0].Path != "/m" || !back.Mounts[0].Writable || !back.DenyNetwork {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestLandlockHelperRunsNothingWithoutAPolicy(t *testing.T) {
	t.Setenv(landlockPolicyEnv, "")
	var out strings.Builder
	if code := LandlockExecMain([]string{"--", "sh", "-c", "echo ran > /dev/stderr"}, &out, &out); code != 125 || !strings.Contains(out.String(), "no policy") {
		t.Fatalf("code %d, output %q", code, out.String())
	}
	t.Setenv(landlockPolicyEnv, "{not json")
	out.Reset()
	if code := LandlockExecMain([]string{"--", "true"}, &out, &out); code != 125 || !strings.Contains(out.String(), "does not parse") {
		t.Fatalf("code %d, output %q", code, out.String())
	}
	out.Reset()
	if code := LandlockExecMain([]string{"true"}, &out, &out); code != 125 || !strings.Contains(out.String(), "usage") {
		t.Fatalf("code %d, output %q", code, out.String())
	}
}
