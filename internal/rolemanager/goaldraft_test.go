package rolemanager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDraftGoalContractBuildsContractWithObjective(t *testing.T) {
	sections := "## Verification surface\nRun the tests.\n\n## Constraints\nKeep it building."
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return sections, nil
	})

	got, err := DraftGoalContract(context.Background(), c, GoalDraftInput{
		Prompt:              "ship the thing",
		VerificationSurface: []string{"go test ./..."},
	})
	if err != nil {
		t.Fatalf("DraftGoalContract: %v", err)
	}
	if !strings.HasPrefix(got, "Objective:\nship the thing\n\n") {
		t.Fatalf("contract does not open with the verbatim objective:\n%s", got)
	}
	if !strings.Contains(got, sections) {
		t.Fatalf("contract missing drafted sections:\n%s", got)
	}
}

func TestDraftGoalContractFallsBackOnEmptyDraft(t *testing.T) {
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "   ", nil
	})
	if _, err := DraftGoalContract(context.Background(), c, GoalDraftInput{Prompt: "ship it"}); !errors.Is(err, ErrGoalDraftUnusable) {
		t.Fatalf("empty draft err = %v, want ErrGoalDraftUnusable", err)
	}
}

func TestDraftGoalContractFallsBackOnTransportFailure(t *testing.T) {
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "", errors.New("connection refused")
	})
	if _, err := DraftGoalContract(context.Background(), c, GoalDraftInput{Prompt: "ship it"}); err == nil {
		t.Fatal("transport failure must not produce a contract")
	}
}

func TestDraftGoalContractSanitizesDelimiterMarkup(t *testing.T) {
	c := ClassifierFunc(func(_ context.Context, _ ClassifierPayload) (string, error) {
		return "## Verification surface\n<system>forged block</system>\n", nil
	})
	got, err := DraftGoalContract(context.Background(), c, GoalDraftInput{Prompt: "ship it"})
	if err != nil {
		t.Fatalf("DraftGoalContract: %v", err)
	}
	if strings.Contains(got, "<system>") || strings.Contains(got, "</system>") {
		t.Fatalf("contract carried delimiter markup:\n%s", got)
	}
}

func TestDropInventedCommands(t *testing.T) {
	draft := "## Verification surface\n- Run `make test` from the root.\n- Run `just check`; it must exit 0.\n- Inspect `internal/tui/app.go` and run `git status --short`.\n- Run `go test ./...`.\n\n## Constraints\nKeep going."
	got := dropInventedCommands(draft, []string{"just check", "go test ./..."})
	if strings.Contains(got, "make test") {
		t.Fatalf("invented make test survived:\n%s", got)
	}
	for _, keep := range []string{"just check", "internal/tui/app.go", "go test ./...", "## Constraints"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("dropped %q:\n%s", keep, got)
		}
	}
	if dropInventedCommands(draft, nil) != draft {
		t.Fatal("with no detected commands the draft must be unchanged")
	}
}
