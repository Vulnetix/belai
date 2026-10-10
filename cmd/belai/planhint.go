package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// planReviewHint is what a headless plan-mode run prints to stderr after it
// has written a plan: where the file is and how to approve, refine or leave it.
// There is nobody to ask, so the three choices of the TUI's review pane are
// given as commands. The path is shown relative to the working directory when
// it is inside it, and both commands quote it.
//
// Approving needs -allow-ask-without-tty. A headless run has no terminal to
// approve each change on, so without it every file change the plan needs is
// refused and the model reports that it could not make them (measured: a
// two-step port change ran to completion with the flag and made no change
// without it). The flag is the user's approval of the plan they just read.
func planReviewHint(path, workdir string) string {
	shown := path
	if rel, err := filepath.Rel(workdir, path); err == nil && !strings.HasPrefix(rel, "..") {
		shown = rel
	}
	var b strings.Builder
	fmt.Fprintf(&b, "plan written: %s\n", path)
	fmt.Fprintf(&b, "  approve: belai -allow-ask-without-tty -prompt %q   (no terminal here to approve each change on, so the flag stands in for it)\n", "@"+shown)
	fmt.Fprintf(&b, "  refine:  belai -plan -prompt %q\n", "Revise @"+shown+": <what to change>")
	b.WriteString("  cancel:  do nothing; the file is only a plan and nothing has been changed\n")
	return b.String()
}
