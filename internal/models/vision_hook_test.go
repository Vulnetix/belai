package models

import "testing"

// The extraVision hook is nil in the default build and consulted only after
// the built-in list.
func TestVisionHook(t *testing.T) {
	saved := extraVision
	t.Cleanup(func() { extraVision = saved })

	extraVision = nil
	if Vision("hooked", "special-vision-id") {
		t.Fatal("an id outside the list is text-only without a hook")
	}
	if !Vision("p", "gpt-5") {
		t.Fatal("the built-in list still answers without a hook")
	}

	extraVision = func(providerName, id string) bool { return providerName == "hooked" && id == "special-vision-id" }
	if !Vision("hooked", "special-vision-id") {
		t.Fatal("the hook should name its own vision id")
	}
	if Vision("other", "special-vision-id") {
		t.Fatal("the hook is handed the provider name")
	}
	if Vision("hooked", "") {
		t.Fatal("an empty id is never vision")
	}
	if !Vision("hooked", "gpt-5") {
		t.Fatal("the hook must not shadow the built-in list")
	}
}
