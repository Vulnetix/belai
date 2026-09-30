package fleet

import (
	"errors"
	"os"
	"testing"
)

func TestCheckCrewFreeRefusesALiveCrewInTheSameRepo(t *testing.T) {
	_, reg := testEnv(t)
	if err := reg.CheckCrewFree("belai:security", "/r"); err != nil {
		t.Fatalf("an empty registry refused: %v", err)
	}
	live := Record{ID: "w-1", PID: os.Getpid(), Profile: "belai:patcher", Crew: "belai:security", Repo: "/r", State: StateWorking}
	if err := reg.Save(live); err != nil {
		t.Fatal(err)
	}
	if err := reg.CheckCrewFree("belai:security", "/r"); !errors.Is(err, ErrCrewRunning) {
		t.Fatalf("err = %v, want ErrCrewRunning", err)
	}
	// Another repository, another crew, or a worker outside any crew is free.
	if err := reg.CheckCrewFree("belai:security", "/other"); err != nil {
		t.Fatalf("other repo refused: %v", err)
	}
	if err := reg.CheckCrewFree("belai:delivery", "/r"); err != nil {
		t.Fatalf("other crew refused: %v", err)
	}
	if err := reg.CheckCrewFree("", "/r"); err != nil {
		t.Fatalf("no crew refused: %v", err)
	}
}

func TestCheckCrewFreeIgnoresFinishedWorkers(t *testing.T) {
	_, reg := testEnv(t)
	done := Record{ID: "w-2", Crew: "belai:security", Repo: "/r", State: StateStopped}
	if err := reg.Save(done); err != nil {
		t.Fatal(err)
	}
	if err := reg.CheckCrewFree("belai:security", "/r"); err != nil {
		t.Fatalf("a stopped worker blocked a start: %v", err)
	}
}
