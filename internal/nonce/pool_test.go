package nonce

import "testing"

func TestSeedAndUniqueness(t *testing.T) {
	p := New()
	if err := p.Seed(1000); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if p.Available() != 1000 {
		t.Fatalf("Available = %d, want 1000", p.Available())
	}
	seen := map[string]bool{}
	for _, s := range p.avail {
		if seen[s] {
			t.Fatalf("duplicate seeded nonce %q", s)
		}
		seen[s] = true
	}
}

func TestReserveRelease(t *testing.T) {
	p := New()
	if err := p.Seed(3); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	n1, err := p.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !p.Valid(n1) {
		t.Fatalf("reserved nonce should be valid")
	}
	if p.Available() != 2 || p.Active() != 1 {
		t.Fatalf("counts after reserve: avail=%d active=%d", p.Available(), p.Active())
	}

	p.Release(n1)
	if p.Valid(n1) {
		t.Fatalf("released nonce should not be valid")
	}
	if p.Available() != 3 || p.Active() != 0 {
		t.Fatalf("counts after release: avail=%d active=%d", p.Available(), p.Active())
	}

	// Re-reserving the released nonce should make it valid again.
	n2, _ := p.Reserve()
	if n2 != n1 {
		t.Fatalf("re-reserved nonce = %q, want %q", n2, n1)
	}
	if !p.Valid(n1) {
		t.Fatalf("re-reserved nonce should be valid")
	}
}

func TestAvailableNonceNotValidUntilReserved(t *testing.T) {
	p := New()
	if err := p.Seed(1); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	avail := p.avail[0]
	if p.Valid(avail) {
		t.Fatalf("unreserved nonce must not be valid")
	}
}

func TestReserveMintsOnEmpty(t *testing.T) {
	p := New()
	n, err := p.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if n == "" {
		t.Fatalf("minted nonce is empty")
	}
	if !p.Valid(n) {
		t.Fatalf("minted nonce should be valid")
	}
}

func TestRotateInvalidatesOldNonces(t *testing.T) {
	p := New()
	if err := p.Seed(2); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	old, err := p.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !p.Valid(old) {
		t.Fatalf("old nonce should be valid before rotate")
	}
	if err := p.Rotate(2); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if p.Valid(old) {
		t.Fatalf("old nonce should be invalid after rotate")
	}
	if p.Available() != 2 || p.Active() != 0 {
		t.Fatalf("counts after rotate: avail=%d active=%d", p.Available(), p.Active())
	}
}

func TestNonceURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://api.openai.com/v1", "https://api.openai.com/v1/nonces"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1/nonces"},
		{"https://api.anthropic.com", "https://api.anthropic.com/v1/nonces"},
		{"https://gateway.example/openai/org/v1", "https://gateway.example/openai/org/v1/nonces"},
	}
	for _, tc := range cases {
		if got := NonceURL(tc.in); got != tc.want {
			t.Fatalf("NonceURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
