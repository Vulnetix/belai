package commands

import (
	"reflect"
	"testing"
)

func TestParseSnapshotsEveryCLIForm(t *testing.T) {
	out := `Snapshot: https://www.vulnetix.com/snapshots/a
  ✓ Memory:   .vulnetix/memory.yaml
  ✓ Snapshot: https://www.vulnetix.com/snapshots/b
  ✓ SAST Snapshot: https://www.vulnetix.com/snapshots/c
Secrets snapshot: https://www.vulnetix.com/snapshots/d
Malscan snapshot: https://www.vulnetix.com/snapshots/e.
License snapshot: https://www.vulnetix.com/snapshots/f
API: 3 binaries stored (0 flagged by the malware corpus)
     https://www.vulnetix.com/snapshots/g
     https://www.vulnetix.com/not-after-api
Snapshot: https://www.vulnetix.com/snapshots/a
Snapshot: https://evil.example.com/phish
Snapshot: http://www.vulnetix.com/plain
Snapshot: https://user@www.vulnetix.com/x
Snapshot: https://www.vulnetix.com.evil.io/x
    https://www.vulnetix.com/resolve/register, then run 'vulnetix auth login'.
`
	got := ParseSnapshots(out)
	want := []Snapshot{
		{"", "https://www.vulnetix.com/snapshots/a"},
		{"", "https://www.vulnetix.com/snapshots/b"},
		{"SAST", "https://www.vulnetix.com/snapshots/c"},
		{"Secrets", "https://www.vulnetix.com/snapshots/d"},
		{"Malscan", "https://www.vulnetix.com/snapshots/e"},
		{"License", "https://www.vulnetix.com/snapshots/f"},
		{"Binary", "https://www.vulnetix.com/snapshots/g"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}
