package changes

import (
	"strconv"
	"strings"
	"testing"
)

const zeros = "0000000000000000000000000000000000000000"
const sha = "1111111111111111111111111111111111111111"

func newFile(path string, body ...string) string {
	return "diff --git a/" + path + " b/" + path + "\nnew file mode 100644\nindex " + zeros + ".." + sha + "\n--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1," + strconv.Itoa(len(body)) + " @@\n+" + strings.Join(body, "\n+") + "\n"
}

func TestParsePatchAcceptsWhatGitWrites(t *testing.T) {
	patch := newFile("a.txt", "one", "two") +
		"diff --git a/main.go b/main.go\nindex " + sha + ".." + sha + " 100644\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,3 @@ func main()\n a\n-b\n+c\n d\n" +
		"diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\nindex " + sha + ".." + zeros + "\n--- a/gone.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n\\ No newline at end of file\n" +
		"diff --git a/run.sh b/run.sh\nold mode 100644\nnew mode 100755\n" +
		"diff --git a/empty b/empty\nnew file mode 100644\nindex " + zeros + ".." + sha + "\n"

	secs, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range secs {
		got = append(got, s.Path+":"+s.Status)
	}
	want := "a.txt:added main.go:modified gone.txt:deleted run.sh:modified empty:added"
	if strings.Join(got, " ") != want {
		t.Fatalf("sections = %v, want %s", got, want)
	}
	if secs[1].Text != "diff --git a/main.go b/main.go\nindex "+sha+".."+sha+" 100644\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,3 @@ func main()\n a\n-b\n+c\n d\n" {
		t.Fatalf("section text = %q", secs[1].Text)
	}
}

func TestParsePatchAcceptsANoNewlineMarkerBetweenOldAndNewLines(t *testing.T) {
	patch := "diff --git a/f b/f\nindex " + sha + ".." + sha + " 100644\n--- a/f\n+++ b/f\n@@ -1 +1,2 @@\n-a\n\\ No newline at end of file\n+a\n+b\n"

	if _, err := ParsePatch(patch); err != nil {
		t.Fatal(err)
	}
}

func TestParsePatchRefusesEverythingOutsideItsGrammar(t *testing.T) {
	ok := newFile("a.txt", "one")
	hdr := "diff --git a/x b/x\nindex " + sha + ".." + sha + " 100644\n--- a/x\n+++ b/x\n"
	for name, patch := range map[string]string{
		"empty":                   "",
		"no trailing newline":     strings.TrimSuffix(ok, "\n"),
		"NUL byte":                strings.Replace(ok, "one", "o\x00ne", 1),
		"rename":                  "diff --git a/a b/b\nsimilarity index 100%\nrename from a\nrename to b\n",
		"names differ":            strings.Replace(ok, "b/a.txt", "b/other.txt", 1),
		"binary patch":            "diff --git a/x b/x\nnew file mode 100644\nindex " + zeros + ".." + sha + "\nGIT binary patch\nliteral 3\n",
		"binary files differ":     "diff --git a/x b/x\nindex " + sha + ".." + sha + " 100644\nBinary files a/x and b/x differ\n",
		"symlink mode":            "diff --git a/x b/x\nnew file mode 120000\nindex " + zeros + ".." + sha + "\n--- /dev/null\n+++ b/x\n@@ -0,0 +1 @@\n+target\n",
		"submodule mode":          "diff --git a/x b/x\nnew file mode 160000\nindex " + zeros + ".." + sha + "\n",
		"dot dot path":            strings.ReplaceAll(ok, "a.txt", "../a.txt"),
		"absolute path":           strings.ReplaceAll(ok, "a.txt", "/etc/passwd"),
		"inside .git":             strings.ReplaceAll(ok, "a.txt", ".git/hooks/pre-commit"),
		"inside .vulnetix":        strings.ReplaceAll(ok, "a.txt", ".vulnetix/belai/x"),
		"quoted path":             "diff --git \"a/x y\" \"b/x y\"\n",
		"same file twice":         ok + ok,
		"stray line":              ok + "hello\n",
		"stray line in headers":   strings.Replace(ok, "new file mode 100644\n", "new file mode 100644\nsomething\n", 1),
		"two index lines":         strings.Replace(ok, "--- /dev/null", "index "+sha+".."+sha+"\n--- /dev/null", 1),
		"names do not match mode": strings.Replace(ok, "--- /dev/null", "--- a/a.txt", 1),
		"--- without +++":         hdr[:strings.Index(hdr, "+++")],
		"hunk without names":      "diff --git a/x b/x\nindex " + sha + ".." + sha + " 100644\n@@ -1 +1 @@\n-a\n+b\n",
		"names without hunk":      hdr,
		"hunk too short":          hdr + "@@ -1,2 +1,2 @@\n-a\n+b\n",
		"hunk too long":           hdr + "@@ -1 +1 @@\n-a\n+b\n+c\n",
		"empty line in hunk":      hdr + "@@ -1,2 +1,2 @@\n-a\n\n+b\n",
		"unknown marker":          hdr + "@@ -1 +1 @@\n-a\n\\ something else\n+b\n",
		"not a hunk line":         hdr + "@@ -1 +1 @@\n-a\n!b\n",
		"smuggled second file":    hdr + "@@ -1 +1 @@\n-a\n+b\ndiff --git a/../../etc/x b/../../etc/x\nindex " + sha + ".." + sha + " 100644\n--- a/../../etc/x\n+++ b/../../etc/x\n@@ -1 +1 @@\n-a\n+b\n",
		"trailing junk":           hdr + "@@ -1 +1 @@\n-a\n+b\nextra\n",
		"count too large":         hdr + "@@ -1,99999999 +1 @@\n-a\n+b\n",
		"bad header":              "diff --git a/x b/x extra\n",
	} {
		if _, err := ParsePatch(patch); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAHunkLineThatLooksLikeAHeaderIsHunkContent(t *testing.T) {
	// "+diff --git ..." inside a hunk is an added line, counted, not a new file.
	patch := newFile("a.txt", "diff --git a/../../etc/x b/../../etc/x", "--- a/x")

	secs, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 1 || secs[0].Path != "a.txt" {
		t.Fatalf("sections = %+v", secs)
	}
}
