package a2a

// Filter modes (the a2a.filter setting).
const (
	// FilterRepo, the default, receives broadcasts about this session's repo.
	FilterRepo = "repo"
	// FilterAll receives every broadcast.
	FilterAll = "all"
	// FilterOff receives nothing; the session still broadcasts.
	FilterOff = "off"
)

// FilterModes lists the filter modes.
func FilterModes() []string { return []string{FilterRepo, FilterAll, FilterOff} }

// NormalizeFilter returns mode when it is a filter mode, else FilterRepo.
func NormalizeFilter(mode string) string {
	switch mode {
	case FilterRepo, FilterAll, FilterOff:
		return mode
	}
	return FilterRepo
}

// Self is what the filter knows of the receiving session.
type Self struct {
	ID   string // its Agent Card id
	Repo string // its Work.Repo
}

// Match reports whether a session should receive env under mode. A
// self-echo is always dropped. Off receives nothing. A message, reply or ack
// is received only by the agent it names. A broadcast is received under all,
// and under repo only when both sides know the repo and it is the same.
func Match(env Envelope, self Self, mode string) bool {
	if env.Agent.ID == "" || env.Agent.ID == self.ID {
		return false
	}
	mode = NormalizeFilter(mode)
	if mode == FilterOff {
		return false
	}
	if env.Directed() {
		return env.To == self.ID
	}
	if mode == FilterAll {
		return true
	}
	return self.Repo != "" && env.Work.Repo == self.Repo
}

// Overlap is the files both lists hold, in a's order.
func Overlap(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	in := make(map[string]bool, len(b))
	for _, f := range b {
		in[f] = true
	}
	var out []string
	for _, f := range a {
		if in[f] {
			out = append(out, f)
		}
	}
	return out
}
