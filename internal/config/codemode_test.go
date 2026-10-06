package config

import "testing"

func cptr[T any](v T) *T { return &v }

func TestCodeDefaultsAndClamp(t *testing.T) {
	var s Settings
	if !s.CodeEnabled() {
		t.Fatal("code mode defaults on")
	}
	if a, b, c := s.CodeLimits(); a != DefaultCodeTimeoutMS || b != DefaultCodeMaxCalls || c != DefaultCodeMaxOutputBytes {
		t.Fatalf("defaults = %d %d %d", a, b, c)
	}
	s.Code = &CodeSettings{TimeoutMS: cptr(10_000_000), MaxCalls: cptr(100000), MaxOutputBytes: cptr(1 << 30)}
	if a, b, c := s.CodeLimits(); a != maxCodeTimeoutMS || b != maxCodeMaxCalls || c != maxCodeMaxOutputBytes {
		t.Fatalf("clamped = %d %d %d", a, b, c)
	}
}

func TestCodeProjectLayerOnlyTightens(t *testing.T) {
	user := mergeCode(nil, &CodeSettings{MaxCalls: cptr(100)}, false)
	got := mergeCode(user, &CodeSettings{Enabled: cptr(true), MaxCalls: cptr(400), TimeoutMS: cptr(999_999)}, true)
	if got.Enabled != nil {
		t.Fatal("a project layer must not turn code mode on")
	}
	if *got.MaxCalls != 100 || got.TimeoutMS != nil {
		t.Fatalf("a project layer raised a limit: %+v", got)
	}
	got = mergeCode(got, &CodeSettings{Enabled: cptr(false), MaxCalls: cptr(10), TimeoutMS: cptr(5000)}, true)
	if got.Enabled == nil || *got.Enabled || *got.MaxCalls != 10 || *got.TimeoutMS != 5000 {
		t.Fatalf("a project layer must be able to tighten: %+v", got)
	}
	if (Settings{Code: got}).CodeEnabled() {
		t.Fatal("code mode must be off")
	}
}
