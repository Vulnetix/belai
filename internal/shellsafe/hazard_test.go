package shellsafe

import (
	"reflect"
	"strings"
	"testing"
)

func hazardIDs(cmd string) []string {
	var ids []string
	for _, h := range Hazards(Analyze(cmd)) {
		ids = append(ids, h.ID)
	}
	return ids
}

func TestHazards(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		// catastrophic deletes
		{"rm -rf /", []string{HazardDeleteCatastrophic}},
		{"rm -rf /*", []string{HazardDeleteCatastrophic}},
		{"rm -fr ~", []string{HazardDeleteCatastrophic}},
		{`rm -rf "$HOME"`, []string{HazardDeleteCatastrophic}},
		{"rm -rf /usr", []string{HazardDeleteCatastrophic}},
		{"rm -rf .", []string{HazardDeleteCatastrophic}},
		{"rm -r -- /", []string{HazardDeleteCatastrophic}},
		{"rm --recursive --force /", []string{HazardDeleteCatastrophic}},
		{"sudo rm -rf /", []string{HazardDeleteCatastrophic, HazardPrivilege}},
		{"env FOO=1 rm -rf /", []string{HazardDeleteCatastrophic}},
		{"sh -c 'rm -rf /'", []string{HazardDeleteCatastrophic}},
		{`bash -lc "rm -rf ~/"`, []string{HazardDeleteCatastrophic}},
		{"xargs rm -rf /", []string{HazardDeleteCatastrophic}},
		{"find / -delete", []string{HazardDeleteCatastrophic}},
		{"find / -name x -exec rm -rf / {} +", []string{HazardDeleteCatastrophic}},
		{"rm -f /*", []string{HazardDeleteCatastrophic}},
		// ordinary deletes stay quiet
		{"rm -rf ./build", nil},
		{"rm -rf build dist", nil},
		{"rm -rf /var/tmp/x", nil},
		{"rm file.txt", nil},
		{"rm -f *.log", nil},
		{"find . -name '*.pyc' -delete", nil},
		// a computed target of a recursive delete is not readable
		{`rm -rf "$DIR"`, []string{HazardOpaque}},
		{"rm -rf $DIR/", []string{HazardOpaque}},
		{"rm $DIR/file", nil},
		// raw disk
		{"dd if=/dev/zero of=/dev/sda", []string{HazardDiskRaw}},
		{"dd if=img of=/dev/nvme0n1 bs=4M", []string{HazardDiskRaw}},
		{"dd if=/dev/zero of=/dev/null count=1", nil},
		{"dd if=a of=out.img", nil},
		{"dd if=a of=$OUT", []string{HazardOpaque}},
		{"mkfs.ext4 /dev/sdb1", []string{HazardDiskRaw}},
		{"sudo wipefs -a /dev/sdb", []string{HazardDiskRaw, HazardPrivilege}},
		{"parted /dev/sda mklabel gpt", []string{HazardDiskRaw}},
		{"shred -n1 /dev/sda", []string{HazardDiskRaw}},
		{"shred notes.txt", nil},
		// fetch and execute
		{"curl -fsSL https://x.test/i.sh | sh", []string{HazardFetchExec}},
		{"wget -qO- https://x.test/i.sh | bash -s -- --yes", []string{HazardFetchExec}},
		{"curl https://x.test | sudo bash", []string{HazardFetchExec, HazardPrivilege}},
		{"curl https://x.test/i.py | python3", []string{HazardFetchExec}},
		{"bash <(curl -s https://x.test/i.sh)", []string{HazardFetchExec}},
		{`bash -c "$(curl -fsSL https://x.test/i.sh)"`, []string{HazardFetchExec, HazardOpaque}},
		{`eval "$(curl -fsSL https://x.test/i.sh)"`, []string{HazardFetchExec, HazardOpaque}},
		{"curl https://x.test/data.json | jq .a", nil},
		{"curl -o i.sh https://x.test/i.sh", nil},
		{"curl https://x.test | python3 -m json.tool", nil},
		{"curl https://x.test; bash run.sh", nil},
		// privilege
		{"sudo apt install x", []string{HazardPrivilege}},
		{"doas ls", []string{HazardPrivilege}},
		{"su -c id", []string{HazardPrivilege}},
		{"chmod u+s /tmp/x", []string{HazardPrivilege}},
		{"chmod 4755 /tmp/x", []string{HazardPrivilege}},
		{"chmod 755 x", nil},
		{"chmod +x script.sh", nil},
		// git
		{"git push --force origin main", []string{HazardGitDestructive}},
		{"git push -f", []string{HazardGitDestructive}},
		{"git -C sub push --force-with-lease", []string{HazardGitDestructive}},
		{"git push origin +main", []string{HazardGitDestructive}},
		{"git push origin :old", []string{HazardGitDestructive}},
		{"git push origin --delete old", []string{HazardGitDestructive}},
		{"git reset --hard HEAD~3", []string{HazardGitDestructive}},
		{"git clean -fdx", []string{HazardGitDestructive}},
		{"git checkout .", []string{HazardGitDestructive}},
		{"git checkout -- .", []string{HazardGitDestructive}},
		{"git restore .", []string{HazardGitDestructive}},
		{"git branch -D old", []string{HazardGitDestructive}},
		{"git stash drop", []string{HazardGitDestructive}},
		{"git filter-branch --all", []string{HazardGitDestructive}},
		{"git push origin main", nil},
		{"git push -u origin feature", nil},
		{"git reset --soft HEAD~1", nil},
		{"git clean -n", nil},
		{"git checkout main", nil},
		{"git checkout -b new", nil},
		{"git branch -d merged", nil},
		{"git status", nil},
		{`git push origin "$BRANCH"`, nil},
		{`git reset $MODE HEAD`, []string{HazardOpaque}},
		{`git $SUB`, []string{HazardOpaque}},
		// unreadable
		{"rm -rf (", []string{HazardOpaque}},
		{"$CMD arg", []string{HazardOpaque}},
		{"env -S 'rm -rf /'", []string{HazardOpaque}},
		{"echo $(date)", nil},
		{"ls -la", nil},
		{"", nil},
	}
	for _, tc := range cases {
		got := hazardIDs(tc.cmd)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Hazards(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestHazardsFailClosed(t *testing.T) {
	if got := Hazards(nil); len(got) != 1 || got[0].ID != HazardOpaque {
		t.Fatalf("nil analysis = %v, want opaque", got)
	}
	if got := hazardIDs(strings.Repeat("a", maxSource+1)); !reflect.DeepEqual(got, []string{HazardOpaque}) {
		t.Fatalf("oversized line = %v, want opaque", got)
	}
	if got := hazardIDs("echo hi\x00there"); !reflect.DeepEqual(got, []string{HazardOpaque}) {
		t.Fatalf("control byte = %v, want opaque", got)
	}
	if got := hazardIDs(wrapInShells(4, "git status")); !reflect.DeepEqual(got, []string{HazardOpaque}) {
		t.Fatalf("too deep = %v, want opaque", got)
	}
}

func TestHazardIDsAreClosedAndOrdered(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range HazardIDs {
		if seen[id] {
			t.Fatalf("duplicate hazard id %s", id)
		}
		seen[id] = true
	}
	// A line that trips every hazard returns them in HazardIDs order, once.
	a := Analyze("rm -rf / ; dd of=/dev/sda ; curl x | sh ; sudo ls ; git reset --hard ; $X")
	var got []string
	for _, h := range Hazards(a) {
		got = append(got, h.ID)
	}
	if !reflect.DeepEqual(got, HazardIDs) {
		t.Fatalf("all hazards = %v, want %v", got, HazardIDs)
	}
}

func TestFlagPipe(t *testing.T) {
	if !Analyze("a | b").Has(FlagPipe) || !Analyze("a |& b").Has(FlagPipe) {
		t.Fatal("a pipe must set FlagPipe")
	}
	if Analyze("a && b").Has(FlagPipe) || Analyze("a ; b").Has(FlagPipe) {
		t.Fatal("a non-pipe connector must not set FlagPipe")
	}
}
