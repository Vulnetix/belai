package rc

import (
	"context"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// maxTeleportBackups bounds how many profiles and crews one teleport_backup
// request uploads, so a request cannot turn into a copy of the whole host.
const maxTeleportBackups = 24

// backupForTeleport answers a teleport_backup request: a person asked, from
// another host, to continue a session that ran here under an agent profile, so
// the library has to hold that profile, every crew that lists it, and those
// crews' members before the other host can install them. The request names one
// profile; the crews and members come from this host's own definitions, never
// from the request. Built-in profiles and crews ship with Belai and are not
// uploaded, and a crew or member that cannot be backed up is reported and does
// not stop the profile's own backup, the only part the teleport needs.
//
// The uploads answer this request, so the server accepts them only while it is
// delivered to this host (the same rule as profile_backup). Nothing here
// installs or changes anything on this host.
func (d *Daemon) backupForTeleport(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	if len(r.Profiles) != 1 {
		return "", "a teleport backup names exactly one profile"
	}
	name := strings.TrimSpace(r.Profiles[0])
	report, why := d.backupProfile(ctx, sessionsync.Dispatch{ID: r.ID, Profile: name})
	if why != "" {
		return "", why
	}
	notes := []string{report}
	done := map[string]bool{name: true}
	uploads := 1
	for _, c := range agentprofile.StoredCrews() {
		if !crewLists(c, name) || uploads >= maxTeleportBackups {
			continue
		}
		uploads++
		if rep, why := d.backupCrew(ctx, sessionsync.Dispatch{ID: r.ID, Crew: c.Name}); why != "" {
			notes = append(notes, "crew "+sanitize.Line(c.Name, 64)+" was not backed up: "+why)
			continue
		} else {
			notes = append(notes, rep)
		}
		for _, m := range c.Members {
			if done[m.Profile] || agentprofile.IsBuiltin(m.Profile) || uploads >= maxTeleportBackups {
				continue
			}
			done[m.Profile] = true
			uploads++
			if rep, why := d.backupProfile(ctx, sessionsync.Dispatch{ID: r.ID, Profile: m.Profile}); why != "" {
				notes = append(notes, "member "+sanitize.Line(m.Profile, 64)+" was not backed up: "+why)
			} else {
				notes = append(notes, rep)
			}
		}
	}
	return strings.Join(notes, "; "), ""
}

func crewLists(c agentprofile.Crew, profile string) bool {
	for _, m := range c.Members {
		if m.Profile == profile {
			return true
		}
	}
	return false
}
