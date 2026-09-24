package tools

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// ReadState is the session's record of which files the model has read, and
// what each looked like on disk when it did. Edit and Write consult it before
// touching an existing file: an edit to a file the model never read is built
// on a guessed old_string, and an edit to a file that changed since the read
// (a formatter, a Bash command, the user) is built on stale bytes. Both are
// refused with a message that says to Read first, which is the guard trained
// harnesses apply.
//
// A nil *ReadState disables the guard, so a tool built by hand (tests, a
// one-off registry) behaves as before.
type ReadState struct {
	mu   sync.Mutex
	seen map[string]fileStamp
}

// fileStamp is what a file looked like when it was read or written.
type fileStamp struct {
	mod  time.Time
	size int64
}

// NewReadState returns an empty read record.
func NewReadState() *ReadState {
	return &ReadState{seen: map[string]fileStamp{}}
}

// Note records abs as read (or just written) in its current on-disk state.
// A partial Read counts: the model saw the file and its trailer names the
// rest.
func (s *ReadState) Note(abs string) {
	if s == nil {
		return
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.seen[abs] = fileStamp{mod: fi.ModTime(), size: fi.Size()}
	s.mu.Unlock()
}

// Check refuses a change to an existing file that was never read, or that
// changed on disk since it was. A file that does not exist yet passes: a new
// file has nothing to be stale against. rel names the file in the message.
func (s *ReadState) Check(abs, rel string) error {
	if s == nil {
		return nil
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	st, ok := s.seen[abs]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("%s has not been read in this session; Read it first, then make the change from the exact bytes you read", rel)
	}
	if !fi.ModTime().Equal(st.mod) || fi.Size() != st.size {
		return fmt.Errorf("%s changed on disk since you last read it (another tool, a formatter or the user wrote to it); Read it again before changing it", rel)
	}
	return nil
}
