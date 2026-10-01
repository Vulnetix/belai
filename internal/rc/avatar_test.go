package rc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/avatar"
	"github.com/vulnetix/belai/internal/pix"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/svgguard"
)

const creatorID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

const creatorJSON = `{"displayName":"Dep Reviewer","palette":["#006860","#00b8a5","#33867f","#66d1c4"],` +
	`"personality":{"report_style":"Findings first.","focus":["exploitable paths"],"vocabulary":["plain"]},"templateVersion":"1"}`

// drawn is Pix with a colour swapped: a drawing the guard admits.
func drawn() []byte { return []byte(strings.ReplaceAll(string(pix.SVG), "rgb(58,196,180)", "#00b8a5")) }

// ackOf waits for the acknowledgement of a request the daemon answers in the
// background.
func (h *libHarness) ackOf(id string) [3]string {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.site.mu.Lock()
		a, ok := h.site.acks[id]
		h.site.mu.Unlock()
		if ok {
			return a
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("%s was never acknowledged", id)
	return [3]string{}
}

func (h *libHarness) avatarRequest(id string) {
	h.d.handle(context.Background(), sessionsync.Dispatch{ID: id, Kind: "avatar", Creator: creatorID})
}

func (h *libHarness) posted() []map[string]string {
	h.site.mu.Lock()
	defer h.site.mu.Unlock()
	return append([]map[string]string(nil), h.site.avatars...)
}

func (h *libHarness) withDrawer(fn func(ctx context.Context, r avatar.Request) ([]byte, string)) {
	h.d.o.DrawAvatar = fn
	h.site.creator = creatorJSON
}

func TestAvatarIsDrawnFromTheWebsitesRequestAndOnlyTheSVGGoesBack(t *testing.T) {
	h := newLibHarness(t)
	var got avatar.Request
	h.withDrawer(func(_ context.Context, r avatar.Request) ([]byte, string) { got = r; return drawn(), "" })

	h.avatarRequest("d1")
	if a := h.ackOf("d1"); a[0] != sessionsync.DispatchStarted || a[2] != "drew the avatar" {
		t.Fatalf("ack = %v", a)
	}
	if got.DisplayName != "Dep Reviewer" || len(got.Palette) != 4 || got.Palette[1] != "#00b8a5" || got.ReportStyle != "Findings first." ||
		len(got.Focus) != 1 || got.Vocabulary[0] != "plain" {
		t.Fatalf("the drawer was given %+v", got)
	}
	posts := h.posted()
	if len(posts) != 1 || posts[0]["dispatch"] != "d1" || posts[0]["refused"] != "" {
		t.Fatalf("posted = %+v", posts)
	}
	// What is posted is the guard's own serialisation, nothing the drawer wrote.
	kept, err := svgguard.Sanitize([]byte(posts[0]["svg"]))
	if err != nil || string(kept) != posts[0]["svg"] {
		t.Fatalf("the posted image is not the guard's bytes: %v", err)
	}
}

func TestAvatarNeverPostsWhatTheGuardRefuses(t *testing.T) {
	h := newLibHarness(t)
	evil := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="96 26 320 320"><script>alert(1)</script></svg>`
	h.withDrawer(func(context.Context, avatar.Request) ([]byte, string) { return []byte(evil), "" })
	h.avatarRequest("d1")

	if a := h.ackOf("d1"); a[0] != sessionsync.DispatchRefused || a[2] != avatar.ReasonUnusable {
		t.Fatalf("ack = %v", a)
	}
	posts := h.posted()
	if len(posts) != 1 || posts[0]["svg"] != "" || posts[0]["refused"] != avatar.ReasonUnusable {
		t.Fatalf("posted = %+v: a refused drawing must go back as a reason only", posts)
	}
	if strings.Contains(posts[0]["refused"], "script") {
		t.Fatal("the reason repeats the drawing")
	}
}

func TestAvatarRefusalsAreReasonsTheWebsiteShows(t *testing.T) {
	h := newLibHarness(t)
	h.site.creator = creatorJSON

	// No model on this host.
	h.avatarRequest("d1")
	if a := h.ackOf("d1"); a[0] != sessionsync.DispatchRefused || a[2] != avatar.ReasonNoModel {
		t.Fatalf("no drawer: %v", a)
	}
	// The drawer's own reason goes back, cleaned.
	h.withDrawer(func(context.Context, avatar.Request) ([]byte, string) {
		return nil, avatar.ReasonSecurity + "\x1b[31m"
	})
	h.avatarRequest("d2")
	if a := h.ackOf("d2"); a[0] != sessionsync.DispatchRefused || a[2] != avatar.ReasonSecurity {
		t.Fatalf("a security refusal: %q", a)
	}
	if posts := h.posted(); len(posts) != 2 || posts[1]["refused"] != avatar.ReasonSecurity {
		t.Fatalf("posted = %+v", posts)
	}
}

func TestAvatarNeedsRemotePromptsAndAnId(t *testing.T) {
	h := newLibHarness(t)
	h.withDrawer(func(context.Context, avatar.Request) ([]byte, string) {
		t.Error("drew when it should not")
		return drawn(), ""
	})

	h.on = false
	h.avatarRequest("d1")
	if a := h.ackOf("d1"); a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "sync.remote_prompts") {
		t.Fatalf("remote_prompts off: %v", a)
	}
	h.on = true
	for i, id := range []string{"", "../x", strings.ToUpper(creatorID)} {
		did := "bad" + string(rune('a'+i))
		h.d.handle(context.Background(), sessionsync.Dispatch{ID: did, Kind: "avatar", Creator: id})
		if a := h.ackOf(did); a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "not an agent creator id") {
			t.Fatalf("creator %q: %v", id, a)
		}
	}
	if h.site.creators != 0 || len(h.posted()) != 0 {
		t.Fatal("a refused request reached the website")
	}
}

func TestAvatarWhenTheWebsiteDoesNotKnowTheRequestOrWillNotTakeTheDrawing(t *testing.T) {
	h := newLibHarness(t)
	h.withDrawer(func(context.Context, avatar.Request) ([]byte, string) { return drawn(), "" })

	h.site.noCreator = true
	h.avatarRequest("d1")
	if a := h.ackOf("d1"); a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "could not read the avatar request") {
		t.Fatalf("an unknown request: %v", a)
	}
	h.site.noCreator, h.site.noAvatar = false, true
	h.avatarRequest("d2")
	if a := h.ackOf("d2"); a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "did not take the avatar") {
		t.Fatalf("a refused post must not read as success: %v", a)
	}
}

func TestAvatarOneAtATimeAndStopsWithTheDaemon(t *testing.T) {
	h := newLibHarness(t)
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(1)
	h.withDrawer(func(ctx context.Context, _ avatar.Request) ([]byte, string) {
		started.Done()
		select {
		case <-release:
			return drawn(), ""
		case <-ctx.Done():
			return nil, avatar.ReasonTooLong
		}
	})
	h.avatarRequest("d1")
	started.Wait()
	h.avatarRequest("d2")
	if a := h.ackOf("d2"); a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "already drawing") {
		t.Fatalf("a second drawing: %v", a)
	}
	close(release)
	if a := h.ackOf("d1"); a[0] != sessionsync.DispatchStarted {
		t.Fatalf("the first drawing: %v", a)
	}
	// The slot is free again.
	h.d.o.DrawAvatar = func(context.Context, avatar.Request) ([]byte, string) { return drawn(), "" }
	h.avatarRequest("d3")
	if a := h.ackOf("d3"); a[0] != sessionsync.DispatchStarted {
		t.Fatalf("after the first finished: %v", a)
	}
	h.d.wg.Wait()
}

func TestAvatarHasATimeLimit(t *testing.T) {
	h := newLibHarness(t)
	old := avatarTimeout
	avatarTimeout = 30 * time.Millisecond
	t.Cleanup(func() { avatarTimeout = old })
	h.withDrawer(func(ctx context.Context, _ avatar.Request) ([]byte, string) {
		<-ctx.Done()
		return nil, avatar.ReasonTooLong
	})
	h.avatarRequest("d1")
	if a := h.ackOf("d1"); a[0] != sessionsync.DispatchRefused || a[2] != avatar.ReasonTooLong {
		t.Fatalf("a drawing that never finishes: %v", a)
	}
}
