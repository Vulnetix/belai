package fleet

import (
	"errors"
	"fmt"
	"os"
	"sync"
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

// Two starts fired together: the check and the spawns are one step, so exactly
// one gets in and the other sees its workers.
func TestWithCrewStartLetsOnlyOneConcurrentStartIn(t *testing.T) {
	_, reg := testEnv(t)
	const starts = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for i := range starts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := reg.WithCrewStart("belai:security", "/r", true, func() error {
				// What Spawn does: leave a live starting record behind.
				return reg.Save(Record{ID: fmt.Sprintf("w-%d", i), Profile: "p", Crew: "belai:security", Repo: "/r", PID: os.Getpid(), State: StateStarting})
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrCrewRunning):
				refused++
			default:
				t.Errorf("start %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || refused != starts-1 {
		t.Fatalf("%d starts got in and %d were refused, want 1 and %d", ok, refused, starts-1)
	}
	// A crew that is not one per repository is serialised but never refused.
	if err := reg.WithCrewStart("belai:other", "/r", false, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
