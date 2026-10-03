package fleet

import "github.com/vulnetix/belai/internal/agentprofile"

// FillCrew returns the profiles a crew start launches to bring the crew back to
// what its file asks for in a repository: for each member, its replicas minus
// the live workers of that profile, in that crew and repository, in crew order.
// `belai agent start -crew NAME -fill` uses it to start a replica that was
// refused or ended without starting the whole crew again.
func FillCrew(c agentprofile.Crew, live []Record, repo string) []string {
	var out []string
	for _, m := range c.Members {
		have := 0
		for _, rec := range live {
			if rec.Crew == c.Name && rec.Repo == repo && rec.Profile == m.Profile && rec.State.Live() {
				have++
			}
		}
		for range m.Count() - have {
			out = append(out, m.Profile)
		}
	}
	return out
}
