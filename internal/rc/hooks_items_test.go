package rc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
	"github.com/vulnetix/belai/internal/sessionsync"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// serveHook makes the site answer an item_install of a hook with a document and the
// files it lists.
func (h *itemHarness) serveHook(name, command string, files map[string]string) map[string]any {
	doc := map[string]any{"name": name, "description": "Guards", "hooks": map[string]any{"PreToolUse": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}}
	h.site.mu.Lock()
	defer h.site.mu.Unlock()
	h.site.name, h.site.body = name, doc
	h.site.files = nil
	h.site.blobs = map[string]string{}
	for p, c := range files {
		h.site.files = append(h.site.files, map[string]any{"path": p, "sha256": sum(c), "sizeBytes": len(c)})
		h.site.blobs[sum(c)] = c
	}
	return doc
}

func TestHookInstallUnpacksTheBundleAndAdvertisesItsHash(t *testing.T) {
	h := newItemHarness(t)
	files := map[string]string{"guard.sh": "#!/bin/sh\necho '{}'\n", "data/rules.txt": "x\n"}
	doc := h.serveHook("guard", "guard.sh data/rules.txt", files)
	status, why := h.install(libitem.Hook, false)
	if status != sessionsync.DispatchStarted {
		t.Fatalf("install: %s %s", status, why)
	}
	dir := filepath.Join(h.home, "hooks", "guard")
	if got, err := os.ReadFile(filepath.Join(dir, "guard.sh")); err != nil || string(got) != files["guard.sh"] {
		t.Fatalf("script = %q %v", got, err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(filepath.Join(dir, "guard.sh")); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("script mode = %v %v", fi.Mode().Perm(), err)
		}
	}
	if !strings.Contains(h.log.String(), "item_install: installed hook guard from version "+itemVer) {
		t.Errorf("log = %q", h.log.String())
	}

	// The inventory advertises the bundle hash, which the library's version hash equals.
	raw, _ := jsonMarshal(doc)
	it, err := libitem.Validate(libitem.Hook, raw)
	if err != nil {
		t.Fatal(err)
	}
	want := libitem.HashBundle(it.Doc, []libitem.BundleFile{{Path: "data/rules.txt", SHA256: sum(files["data/rules.txt"])}, {Path: "guard.sh", SHA256: sum(files["guard.sh"])}})
	var advertised string
	for _, i := range localInventoryItems() {
		if i.Kind == "hook" && i.Name == "guard" {
			advertised = i.SHA256
		}
	}
	if advertised != want {
		t.Errorf("advertised %q, want %q", advertised, want)
	}
	// An edited script changes what the host advertises: that is drift.
	if err := os.WriteFile(filepath.Join(dir, "guard.sh"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, i := range localInventoryItems() {
		if i.Kind == "hook" && i.SHA256 == want {
			t.Error("an edited script kept the advertised hash")
		}
	}
	// A second install needs replace, as any item does.
	if status, why := h.install(libitem.Hook, false); status != sessionsync.DispatchRefused || !strings.Contains(why, "replace turned on") {
		t.Errorf("second install: %s %q", status, why)
	}
	if status, _ := h.install(libitem.Hook, true); status != sessionsync.DispatchStarted {
		t.Error("an install with replace was refused")
	}
}

func TestHookInstallRefusals(t *testing.T) {
	h := newItemHarness(t)
	// A program the user did not allow.
	h.serveHook("pix", "vulnetix agent hook", nil)
	if status, why := h.install(libitem.Hook, false); status != sessionsync.DispatchRefused || !strings.Contains(why, "hooks.allowed_programs") {
		t.Fatalf("an unallowed program: %s %q", status, why)
	}
	// A file the library lists but does not serve.
	h.serveHook("guard", "guard.sh", map[string]string{"guard.sh": "x\n"})
	h.site.mu.Lock()
	h.site.blobs = map[string]string{}
	h.site.mu.Unlock()
	if status, why := h.install(libitem.Hook, false); status != sessionsync.DispatchRefused || !strings.Contains(why, "could not read a file") {
		t.Fatalf("a missing blob: %s %q", status, why)
	}
	// Served content that does not hash to the listed hash.
	h.serveHook("guard", "guard.sh", map[string]string{"guard.sh": "x\n"})
	h.site.mu.Lock()
	for k := range h.site.blobs {
		h.site.blobs[k] = "tampered\n"
	}
	h.site.mu.Unlock()
	if status, why := h.install(libitem.Hook, false); status != sessionsync.DispatchRefused || !strings.Contains(why, "does not match") {
		t.Fatalf("tampered content: %s %q", status, why)
	}
	// More files than a bundle holds.
	many := map[string]string{}
	for i := 0; i <= libstore.MaxBundleFiles; i++ {
		many[strings.Repeat("a", i+1)] = "x\n"
	}
	h.serveHook("guard", "aa", many)
	if status, why := h.install(libitem.Hook, false); status != sessionsync.DispatchRefused || !strings.Contains(why, "the most is") {
		t.Fatalf("too many files: %s %q", status, why)
	}
	// The switch closes the kind.
	h.on[libitem.Hook] = false
	h.serveHook("guard", "guard.sh", map[string]string{"guard.sh": "x\n"})
	if status, why := h.install(libitem.Hook, false); status != sessionsync.DispatchRefused || !strings.Contains(why, "sync.hooks is off") {
		t.Fatalf("the switch: %s %q", status, why)
	}
	h.on[libitem.Hook] = true
	if _, err := os.Stat(filepath.Join(h.home, "hooks", "guard")); err == nil {
		t.Error("a refused install left a bundle")
	}
	// A hook is never backed up from a host.
	status, why := h.run(sessionsync.Dispatch{Kind: "item_backup", ItemKind: "hook", Name: "guard"})
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "never backed up") {
		t.Errorf("backup: %s %q", status, why)
	}
}

func TestHooksAreNotPartOfTheAutomaticSync(t *testing.T) {
	h := newItemHarness(t)
	h.serveHook("guard", "guard.sh", map[string]string{"guard.sh": "x\n"})
	if status, why := h.install(libitem.Hook, false); status != sessionsync.DispatchStarted {
		t.Fatalf("install: %s %s", status, why)
	}
	for _, k := range h.d.itemKindsOn() {
		if k == libitem.Hook {
			t.Fatal("the sync would ask about hooks")
		}
	}
	if len(localItems(false, h.d.itemKindsOn())) != 0 {
		t.Error("a hook is among the items the sync pushes")
	}
	// The hook is still held and advertised: the inventory and the sync are different lists.
	found := false
	for _, i := range localInventoryItems() {
		found = found || i.Kind == "hook"
	}
	if !found {
		t.Error("the inventory does not list the installed hook")
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
