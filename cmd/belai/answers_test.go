package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/vulnetix/belai/internal/clarify"
)

func questionnaire(context string) clarify.Questionnaire {
	return clarify.Questionnaire{Groups: []clarify.Group{{Context: context, Options: []clarify.Option{{Label: "A"}, {Label: "B"}}}}}
}

func TestAnswerScriptOrderMatchAndExhaustion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.json")
	body := `[{"skip": true}, {"match": "stuck|progress", "choose": 1, "note": "try another way"}, {"choose": 0, "note": "second"}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadAnswerScript(path, io.Discard)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if a := s.answer(questionnaire("Which format?")); !a.SkippedAll() {
		t.Fatalf("first question takes the first positional entry (skip): %+v", a)
	}
	a := s.answer(questionnaire("The goal has stopped making progress. How should it proceed?"))
	if a.Items[0].Chosen[0] != 1 || a.Items[0].Note != "try another way" {
		t.Fatalf("a matching question takes the match entry: %+v", a)
	}
	if a := s.answer(questionnaire("Which file?")); a.Items[0].Note != "second" {
		t.Fatalf("the next positional entry follows: %+v", a)
	}
	if a := s.answer(questionnaire("Anything else?")); !a.SkippedAll() {
		t.Fatalf("an exhausted script declines: %+v", a)
	}
}

func TestAnswerScriptRejectsBadFiles(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"unknown key":  `[{"pick": 1}]`,
		"negative":     `[{"choose": -1}]`,
		"bad match":    `[{"match": "("}]`,
		"not an array": `{"choose": 0}`,
	} {
		path := filepath.Join(dir, "a.json")
		_ = os.WriteFile(path, []byte(body), 0o600)
		if _, err := loadAnswerScript(path, io.Discard); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
