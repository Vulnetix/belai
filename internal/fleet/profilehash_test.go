package fleet

import (
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
)

// Stamping an id or recolouring an agent must not stop a worker that runs on
// the old definition, while a change to what it does still must.
func TestProfileHashIgnoresPresentationOnly(t *testing.T) {
	base := agentprofile.AgentProfile{
		Name: "scout", Description: "d", SystemPrompt: "Look.", Mode: agentprofile.ModeSingle,
	}
	want := ProfileHash(base)

	shown := base
	shown.ID = "0b6f5d1e-3c52-4c0e-9d1a-6f1c7a2b8e34"
	shown.DisplayName = "Scout"
	shown.Palette = []string{"#006860", "#00b8a5", "#33867f", "#66d1c4"}
	shown.AvatarID = "5f1d7c52-0a47-4f4e-8a56-0d0b8c3a7e11"
	if got := ProfileHash(shown); got != want {
		t.Fatalf("presentation changed the hash: %s != %s", got, want)
	}

	styled := base
	styled.Personality = &agentprofile.Personality{ReportStyle: "Brief."}
	if ProfileHash(styled) == want {
		t.Fatal("a personality changes the prompt, so it must change the hash")
	}
	edited := base
	edited.SystemPrompt = "Look harder."
	if ProfileHash(edited) == want {
		t.Fatal("a prompt edit must change the hash")
	}
}
