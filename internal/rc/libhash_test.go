package rc

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/libinstall"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// consoleMarkdown is an agent as the website's editor writes it (profileMarkdown
// in useBelaiAgentBuilder.ts): keys in the editor's own order, which puts
// knowledge before kanban where Belai writes it last, and values in JavaScript's
// JSON form, which leaves < > & as they are where Go escapes them. The library
// hashes these exact bytes.
const consoleMarkdown = `---
name: "triage"
id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
description: "Sorts <new> issues & labels them"
tools: ["Read","Grep"]
mode: "worker"
autonomy: "supervised"
knowledge: {"paths":["docs"]}
kanban: {"lists":["backlog"],"assigned_only":true,"on_success":{"list":"review"},"on_failure":{"list":"backlog"}}
---
You triage.
`

type fakeSource struct{ md string }

func (f fakeSource) Profile(context.Context, string, string) (string, []sessionsync.FileRef, error) {
	return f.md, nil, nil
}
func (f fakeSource) File(context.Context, string) ([]byte, error) { return nil, nil }
func (f fakeSource) Crew(context.Context, string, string) (sessionsync.CrewFetched, error) {
	return sessionsync.CrewFetched{}, nil
}

// An agent written by the console does not render back to the bytes the library
// holds, so the hash of the host's render is not any library version's hash.
// This is why the host keeps the library's hash for what it installed.
func TestARenderOfAConsoleAgentIsNotTheBytesTheLibraryHolds(t *testing.T) {
	p, err := agentprofile.ParseMarkdown([]byte(consoleMarkdown))
	if err != nil {
		t.Fatal(err)
	}
	md, err := agentprofile.MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(md) == consoleMarkdown {
		t.Fatal("the render equals the console's bytes: the host hash needs no mapping, and this test's premise is gone")
	}
	// Go escapes the angle brackets and the ampersand inside a string; the console leaves them as they are.
	if !strings.Contains(string(md), "\\u003cnew\\u003e") {
		t.Errorf("expected Go's escaping in the render:\n%s", md)
	}
}

// Installing a library version records the library's hash for it. While the
// profile is unedited the inventory and the sync report that hash, so the panel
// reads the agent as current and the server answers current, not push. After an
// edit on the host they report the hash of what is there.
func TestAnInstalledAgentReportsTheLibraryHashUntilItIsEdited(t *testing.T) {
	home(t)
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	in := libinstall.Installer{Src: fakeSource{md: consoleMarkdown}, Fetched: recordLibraryHash}
	if _, _, why := in.Profile(context.Background(), id, "202610050900", false); why != "" {
		t.Fatal(why)
	}
	libraryHash := hashOf([]byte(consoleMarkdown))

	find := func() sessionsync.RCProfile {
		for _, x := range LocalInventory().Profiles {
			if x.Name == "triage" {
				return x
			}
		}
		t.Fatal("triage is not advertised")
		return sessionsync.RCProfile{}
	}
	if got := find().SHA256; got != libraryHash {
		t.Fatalf("an unedited install reports %q, want the library's %q", got, libraryHash)
	}

	p, err := agentprofile.Load("triage")
	if err != nil {
		t.Fatal(err)
	}
	p.Description = "edited on the host"
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	edited, _ := agentprofile.Load("triage")
	md, _ := profileDocument(edited)
	if got := find().SHA256; got == libraryHash || got != hashOf(md) {
		t.Fatalf("an edited profile reports %q, want its own render %q", got, hashOf(md))
	}
}

// The sync asks the server with the same hash the inventory reports.
func TestSyncAsksWithTheLibraryHashForAnUneditedInstall(t *testing.T) {
	home(t)
	const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	in := libinstall.Installer{Src: fakeSource{md: consoleMarkdown}, Fetched: recordLibraryHash}
	if _, _, why := in.Profile(context.Background(), id, "202610050900", false); why != "" {
		t.Fatal(why)
	}
	p, _ := agentprofile.Load("triage")
	md, ok := profileDocument(p)
	if !ok {
		t.Fatal("not synced")
	}
	held := readLibraryHashes()
	if got, want := hashFor(held, id, hashOf(md)), hashOf([]byte(consoleMarkdown)); got != want {
		t.Fatalf("sync hash = %q, want %q", got, want)
	}
	// Something else this host holds under that id is not the installed copy.
	if got := hashFor(held, id, "ffff"); got != "ffff" {
		t.Fatalf("a render that is not the installed one reports %q", got)
	}
	if got := hashFor(held, "other", "abcd"); got != "abcd" {
		t.Fatalf("a profile with no record reports %q", got)
	}
}

// A render that is the library's own bytes needs no record.
func TestNoRecordIsKeptWhenTheRenderIsTheLibraryBytes(t *testing.T) {
	home(t)
	recordLibraryHash("agent", "id1", []byte("same"), []byte("same"))
	if got := readLibraryHashes(); len(got) != 0 {
		t.Fatalf("records = %+v", got)
	}
}
