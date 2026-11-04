package fleet

import (
	"context"
	"reflect"
	"testing"
)

// noIdentity is a git runner for a repository with no user.name or user.email.
func noIdentity(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func TestIdentityEnvSuppliesAnIdentityOnAnUnconfiguredMachine(t *testing.T) {
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")
	got := identityEnv(context.Background(), noIdentity, t.TempDir())
	want := []string{
		"GIT_AUTHOR_NAME=Belai agent", "GIT_AUTHOR_EMAIL=agent@belai.invalid",
		"GIT_COMMITTER_NAME=Belai agent", "GIT_COMMITTER_EMAIL=agent@belai.invalid",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The Pix sandbox names the committer in the environment; the worker must not
// replace it, or the commit signer refuses the commit.
func TestIdentityEnvNeverReplacesACommitterTheEnvironmentSets(t *testing.T) {
	t.Setenv("GIT_COMMITTER_NAME", "Pix [bot]")
	t.Setenv("GIT_COMMITTER_EMAIL", "security@vulnetix.com")
	got := identityEnv(context.Background(), noIdentity, t.TempDir())
	want := []string{"GIT_AUTHOR_NAME=Belai agent", "GIT_AUTHOR_EMAIL=agent@belai.invalid"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	t.Setenv("GIT_COMMITTER_EMAIL", "")
	got = identityEnv(context.Background(), noIdentity, t.TempDir())
	want = append(want, "GIT_COMMITTER_EMAIL=agent@belai.invalid")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("only the unset committer variable is supplied: got %v, want %v", got, want)
	}
}

func TestIdentityEnvLeavesAConfiguredIdentityAlone(t *testing.T) {
	configured := func(_ context.Context, _ string, argv ...string) ([]byte, error) {
		return []byte("someone\n"), nil
	}
	if got := identityEnv(context.Background(), configured, t.TempDir()); got != nil {
		t.Fatalf("got %v, want nothing", got)
	}
}
