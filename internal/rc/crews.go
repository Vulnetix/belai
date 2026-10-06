package rc

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/libinstall"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The crew library, the counterpart of the agent library (profiles.go). Two more
// requests from the website reach a crew on this host, both through the dispatch
// queue and both carrying identifiers only:
//
//	crew_backup   export the named crew as JSON and upload it
//	crew_install  read one version of a library crew, install the member
//	              profiles it lists that this host lacks (with their files and
//	              index), then write the crew
//
// A crew holds no settings of its own: every member is an ordinary profile,
// validated on its own. So an install brings the members first, and a crew whose
// member cannot be installed is not written at all. A member this host already
// has is left alone unless the request says to replace.

// backupCrew exports the crew the request names and uploads it.
func (d *Daemon) backupCrew(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	name := strings.TrimSpace(r.Crew)
	if name == "" || len(name) > 128 || sanitize.Line(name, 128) != name {
		return "", "that is not a crew name"
	}
	if agentprofile.IsBuiltin(name) {
		return "", "built-in crews ship with Belai, so there is nothing to back up"
	}
	c, ok := storedCrew(name)
	if !ok {
		return "", "this host has no crew " + sanitize.Line(name, 64)
	}
	if !agentprofile.ValidID(c.ID) {
		// A crew file dropped in by hand has no id yet. Give it one so the
		// library can tell it from any other.
		if _, err := agentprofile.EnsureCrewIDs(); err != nil {
			return "", "could not give the crew an id: " + reason(err.Error())
		}
		if c, ok = storedCrew(name); !ok || !agentprofile.ValidID(c.ID) {
			return "", "could not give the crew an id"
		}
	}
	js, err := c.CanonicalJSON()
	if err != nil {
		return "", "could not export the crew: " + reason(err.Error())
	}
	version, err := d.o.Client.CrewBackup(ctx, d.o.HostID, r.ID, js)
	if err != nil {
		return "", "the library did not take the crew: " + reason(err.Error())
	}
	d.markSynced("crew", c.ID, js)
	return fmt.Sprintf("backed up crew %s as version %s", sanitize.Line(name, 64), sanitize.Ident(version, 12)), ""
}

func storedCrew(name string) (agentprofile.Crew, bool) { return libinstall.StoredCrew(name) }

// installCrew reads the library crew version the request names and writes it as
// a crew on this host, members first, or says why it did not.
func (d *Daemon) installCrew(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	return d.installer(r.ID).Crew(ctx, r.Library, r.Version, r.Overwrite)
}
