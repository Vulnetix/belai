package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseClaim(t *testing.T) {
	for _, c := range []struct {
		in           string
		lists, label []string
		bad          bool
	}{
		{"", nil, nil, false},
		{"review", []string{"review"}, nil, false},
		{"backlog:build", []string{"backlog"}, []string{"build"}, false},
		{" review : needs-review , docs ", []string{"review"}, []string{"needs-review", "docs"}, false},
		{":build", nil, nil, true},
		{"backlog,review", nil, nil, true},
		{"backlog:", nil, nil, true},
	} {
		lists, labels, err := parseClaim(c.in)
		if (err != nil) != c.bad || !reflect.DeepEqual(lists, c.lists) || !reflect.DeepEqual(labels, c.label) {
			t.Errorf("parseClaim(%q) = %v %v %v", c.in, lists, labels, err)
		}
	}
}

func adhocFor(t *testing.T, args ...string) (adhocFlags, error) {
	t.Helper()
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	a := addAdhocFlags(fs)
	return a, fs.Parse(args)
}

func TestAdhocFlagsBuildAWorkerOnlyWithAPrompt(t *testing.T) {
	a, err := adhocFor(t)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := a.profile(); p != nil || err != nil {
		t.Fatalf("no -prompt, no worker: %v %v", p, err)
	}

	a, _ = adhocFor(t, "-tools", "Read")
	if _, err := a.profile(); err == nil {
		t.Fatal("-tools without -prompt was accepted")
	}
	a, _ = adhocFor(t, "-read-only")
	if _, err := a.profile(); err == nil {
		t.Fatal("-read-only without -prompt was accepted")
	}
	a, _ = adhocFor(t, "-prompt", "  ")
	if _, err := a.profile(); err == nil {
		t.Fatal("a blank -prompt was accepted")
	}

	a, _ = adhocFor(t, "-prompt", "Fix it.", "-tools", "Read, Edit", "-claim", "backlog:build", "-to", "review", "-publish", "none")
	p, err := a.profile()
	if err != nil || p == nil {
		t.Fatalf("%v %v", p, err)
	}
	if !p.Ephemeral || p.SystemPrompt != "Fix it." || !reflect.DeepEqual(p.Tools, []string{"Read", "Edit"}) ||
		!reflect.DeepEqual(p.Kanban.Labels, []string{"build"}) || p.Kanban.OnSuccess.List != "review" || p.PublishMode() != "none" {
		t.Fatalf("profile %+v kanban %+v", p, p.Kanban)
	}
}
