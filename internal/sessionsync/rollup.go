package sessionsync

import "github.com/vulnetix/belai/internal/sessionmeasure"

// rollupOf adds up the session file at path for the end call
// (docs/session-measures.md). A session whose file cannot be read ends without
// a rollup, as an older host's does; the server then stores nothing for it.
func rollupOf(path string) *sessionmeasure.Rollup {
	if path == "" {
		return nil
	}
	r, err := sessionmeasure.RollupFile(path)
	if err != nil {
		return nil
	}
	return r
}
