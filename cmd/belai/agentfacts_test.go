package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentValidateWarnsAboutAFactTypo(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir := t.TempDir()
	file := filepath.Join(dir, "ops.md")
	md := "---\nname: ops\ndescription: d\nfacts:\n  aws_role_arns: arn:aws:iam::123456789012:role/ReadOnly\n  environment: prod\n---\nPrompt\n"
	if err := os.WriteFile(file, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runAgentCLI(context.Background(), []string{"validate", file}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("a typo in a fact key must not fail validation; exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "valid") {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(errb.String(), "warning") || !strings.Contains(errb.String(), `"aws_role_arn"`) {
		t.Errorf("stderr should name the likely key, got %q", errb.String())
	}

	bad := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(bad, []byte("---\nname: bad\ndescription: d\nfacts:\n  db_password: x\n---\nPrompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := runAgentCLI(context.Background(), []string{"validate", bad}, strings.NewReader(""), &out, &errb); code == 0 {
		t.Fatal("a secret-named fact must fail validation")
	}
}
