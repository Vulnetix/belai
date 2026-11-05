package agent

import "testing"

func fp(s string) [8]byte { return stuckFingerprint(s, "{}", "same") }

func TestStuckRepeatNudgesThenStops(t *testing.T) {
	d := newStuckDetector(4, 12)
	for i := 0; i < 3; i++ {
		if v := d.observe(fp("Bash")); v != stuckNone {
			t.Fatalf("call %d = %v, want none", i, v)
		}
	}
	if v := d.observe(fp("Bash")); v != stuckNudge {
		t.Fatalf("fourth identical call = %v, want nudge", v)
	}
	for i := 0; i < 3; i++ {
		d.observe(fp("Bash"))
	}
	if v := d.observe(fp("Bash")); v != stuckStop {
		t.Fatalf("second loop = %v, want stop", v)
	}
}

func TestStuckAlternationIsALoop(t *testing.T) {
	d := newStuckDetector(4, 12)
	var got stuckVerdict
	for i := 0; i < stuckAlternation; i++ {
		name := "A"
		if i%2 == 1 {
			name = "B"
		}
		got = d.observe(fp(name))
	}
	if got != stuckNudge {
		t.Fatalf("A,B,A,B,A,B = %v, want nudge", got)
	}
}

func TestStuckChangedResultIsNotARepeat(t *testing.T) {
	d := newStuckDetector(3, 12)
	for i := 0; i < 10; i++ {
		if v := d.observe(stuckFingerprint("Bash", "{}", string(rune('a'+i)))); v != stuckNone {
			t.Fatalf("distinct results flagged at %d", i)
		}
	}
}

func TestStuckResetOnFileChange(t *testing.T) {
	d := newStuckDetector(3, 12)
	d.observe(fp("Bash"))
	d.observe(fp("Bash"))
	d.reset()
	if v := d.observe(fp("Bash")); v != stuckNone {
		t.Fatalf("after reset = %v, want none", v)
	}
}

func TestStuckWindowEvicts(t *testing.T) {
	d := newStuckDetector(3, 6)
	d.observe(fp("Bash"))
	d.observe(fp("Bash"))
	for i := 0; i < 6; i++ {
		d.observe(stuckFingerprint("Read", "{}", string(rune('a'+i))))
	}
	if v := d.observe(fp("Bash")); v != stuckNone {
		t.Fatalf("evicted repeats still counted: %v", v)
	}
}

func TestStuckFingerprintKeepsNoContent(t *testing.T) {
	a := stuckFingerprint("Bash", `{"command":"x"}`, "out")
	if len(a) != 8 {
		t.Fatalf("fingerprint is %d bytes, want 8", len(a))
	}
	if stuckFingerprint("Bash", `{"command":"x"}`, "out") != a || stuckFingerprint("Bash", `{"command":"y"}`, "out") == a {
		t.Fatal("fingerprint is not a stable digest of name, args and result")
	}
}
