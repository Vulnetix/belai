package proc

import (
	"os"
	"testing"
)

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("this process reads as dead")
	}
	if Alive(0) || Alive(-1) {
		t.Fatal("a non-positive pid reads as alive")
	}
	if Alive(1 << 30) {
		t.Fatal("an impossible pid reads as alive")
	}
}
