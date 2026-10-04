//go:build belai_sandbox

package models

import "testing"

func TestSandboxVisionOnlyOnPixSmart(t *testing.T) {
	if !Vision("builtin", "pix-smart") {
		t.Error("pix-smart should accept images")
	}
	if Vision("builtin", "pix-fast") {
		t.Error("pix-fast is text-only")
	}
	if Vision("other", "pix-smart") {
		t.Error("only the builtin provider's pix-smart is vision")
	}
	if !Vision("openai", "gpt-5") {
		t.Error("the stock list must still answer")
	}
}
