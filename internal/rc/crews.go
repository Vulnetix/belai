package rc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
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

func storedCrew(name string) (agentprofile.Crew, bool) {
	for _, c := range agentprofile.StoredCrews() {
		if c.Name == name {
			return c, true
		}
	}
	return agentprofile.Crew{}, false
}

// installCrew reads the library crew version the request names and writes it as
// a crew on this host, members first, or says why it did not.
func (d *Daemon) installCrew(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	if !agentprofile.ValidID(r.Library) || !versionPattern.MatchString(r.Version) {
		return "", "that is not a library crew and version"
	}
	got, err := d.o.Client.CrewFetch(ctx, d.o.HostID, r.Library, r.Version, r.ID)
	if err != nil {
		return "", "could not read the crew from the library: " + reason(err.Error())
	}
	var c agentprofile.Crew
	dec := json.NewDecoder(bytes.NewReader(got.Crew))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return "", "the crew is not valid here: " + reason(err.Error())
	}
	c.Builtin = false
	if c.ID != r.Library {
		return "", "the crew is not the one the request named"
	}
	if why := crewConflict(c, r.Overwrite); why != "" {
		return "", why
	}
	members, why := d.crewMembers(c, got.Members, r.Overwrite)
	if why != "" {
		return "", why
	}
	// Members first: a crew cannot be written until each member is a worker
	// profile on this host. Nothing of the crew is written until every one is.
	var installed, kept []string
	for _, m := range members {
		if !m.install {
			kept = append(kept, sanitize.Line(m.ref.Profile, 64))
			continue
		}
		_, _, why := d.installOne(ctx, r.ID, m.ref.Library, m.ref.Version, m.overwrite)
		if why != "" {
			return "", fmt.Sprintf("could not install member %s, so the crew was not written (%d member%s installed first): %s",
				sanitize.Line(m.ref.Profile, 64), len(installed), plural(len(installed), "", "s"), why)
		}
		installed = append(installed, sanitize.Line(m.ref.Profile, 64))
	}
	if _, err := agentprofile.SaveCrew(c); err != nil {
		return "", "could not save the crew: " + reason(err.Error())
	}
	if js, err := c.CanonicalJSON(); err == nil {
		d.markSynced("crew", c.ID, js)
	}
	report := fmt.Sprintf("installed crew %s from version %s", sanitize.Line(c.Name, 64), r.Version)
	if len(installed) > 0 {
		report += "; installed " + strings.Join(installed, ", ")
	}
	if len(kept) > 0 {
		report += "; kept " + strings.Join(kept, ", ")
	}
	return report, ""
}

// crewMember is one member a crew install puts on the host, or leaves.
type crewMember struct {
	ref       sessionsync.CrewMemberRef
	install   bool
	overwrite bool
}

// crewMembers checks the member profiles a request lists against the crew and
// this host, before anything is written. A member must be one the crew names; one
// this host already has under the same id is left alone (or replaced, when asked);
// one it has under another id is a different profile and refuses the install.
func (d *Daemon) crewMembers(c agentprofile.Crew, refs []sessionsync.CrewMemberRef, overwrite bool) ([]crewMember, string) {
	if len(refs) > agentprofile.MaxCrewMembers {
		return nil, "the request lists more members than a crew has"
	}
	listed := map[string]bool{}
	for _, m := range c.Members {
		listed[m.Profile] = true
	}
	var out []crewMember
	seen := map[string]bool{}
	for _, ref := range refs {
		if !agentprofile.ValidID(ref.Library) || !versionPattern.MatchString(ref.Version) || !listed[ref.Profile] || seen[ref.Profile] {
			return nil, "the request lists a member the crew does not have"
		}
		seen[ref.Profile] = true
		existing, err := agentprofile.Load(ref.Profile)
		switch {
		case err != nil:
			out = append(out, crewMember{ref: ref, install: true})
		case existing.ID != ref.Library:
			return nil, "this host's profile " + sanitize.Line(ref.Profile, 64) + " is a different profile (its id differs) from the crew's member, so the crew was not installed"
		case overwrite:
			out = append(out, crewMember{ref: ref, install: true, overwrite: true})
		default:
			out = append(out, crewMember{ref: ref})
		}
	}
	return out, ""
}

// crewConflict refuses an install that would replace a crew it was not told to,
// the way installConflict does for a profile: the same name is replaced only with
// overwrite set and only when it has the same id, and a crew that holds the same
// id under another name is never replaced.
func crewConflict(c agentprofile.Crew, overwrite bool) string {
	if agentprofile.IsBuiltin(c.Name) {
		return "refused: that name is reserved for a built-in crew"
	}
	if holder, ok := agentprofile.CrewByID(c.ID); ok && holder.Name != c.Name {
		return "refused: this host already has a crew with that id under the name " + sanitize.Line(holder.Name, 64)
	}
	existing, ok := storedCrew(c.Name)
	switch {
	case !ok:
		return ""
	case !overwrite:
		return "this host already has a crew named " + sanitize.Line(c.Name, 64) + "; install it again with replace turned on to overwrite it"
	case existing.ID != c.ID:
		return "refused: the crew of that name here is a different crew (its id differs), so it is not replaced"
	}
	return ""
}
