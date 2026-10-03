package fleet

import (
	"slices"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
)

func TestFillCrewStartsOnlyTheMissingReplicas(t *testing.T) {
	c := agentprofile.Crew{Name: "aws-infra", Members: []agentprofile.Member{{Profile: "log"}, {Profile: "tf", Replicas: 2}, {Profile: "verify"}}}
	live := []Record{
		{ID: "w1", Profile: "log", Crew: "aws-infra", Repo: "/src/infra", State: StateWorking},
		{ID: "w2", Profile: "tf", Crew: "aws-infra", Repo: "/src/infra", State: StateIdle},
		// Not this crew, not this repository, or not live: none of them fills a slot.
		{ID: "w3", Profile: "tf", Crew: "other", Repo: "/src/infra", State: StateWorking},
		{ID: "w4", Profile: "verify", Crew: "aws-infra", Repo: "/src/api", State: StateWorking},
		{ID: "w5", Profile: "verify", Crew: "aws-infra", Repo: "/src/infra", State: StateFailed},
	}

	got := FillCrew(c, live, "/src/infra")
	if want := []string{"tf", "verify"}; !slices.Equal(got, want) {
		t.Fatalf("FillCrew = %v, want %v", got, want)
	}
	if got := FillCrew(c, nil, "/src/infra"); len(got) != 4 {
		t.Fatalf("an empty repository fills the whole crew, got %v", got)
	}
	full := append(live, Record{ID: "w6", Profile: "tf", Crew: "aws-infra", Repo: "/src/infra", State: StatePaused},
		Record{ID: "w7", Profile: "verify", Crew: "aws-infra", Repo: "/src/infra", State: StateStarting})
	if got := FillCrew(c, full, "/src/infra"); len(got) != 0 {
		t.Fatalf("a full crew fills nothing, got %v", got)
	}
}
