// Package libinstall puts a library agent profile or crew on this host.
//
// Two callers share it. The rc daemon installs what a website request names
// (profile_install, crew_install), and teleport installs what a session's
// profile and crews need on the host that continues it. Both read the text
// from the backend, which is untrusted: the markdown is parsed strictly and the
// profile validated whole, a profile never replaces one it was not told to, a
// crew is written only after every member is a profile on this host, and a
// refusal is harness words with a short cleaned excerpt, never the document.
//
// Where the text comes from is the caller's: a Source reads a library version
// through whatever route gates it (a delivered dispatch, a ready teleport).
package libinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/agentfiles"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// VersionPattern is a library version: the UTC minute it was saved.
var VersionPattern = regexp.MustCompile(`^\d{12}$`)

// Source reads library versions for one gated request.
type Source interface {
	// Profile reads one profile version: its markdown and the files it carries.
	Profile(ctx context.Context, library, version string) (markdown string, files []sessionsync.FileRef, err error)
	// File reads one file a profile version carries.
	File(ctx context.Context, sha string) ([]byte, error)
	// Crew reads one crew version and the member profile versions that go with it.
	Crew(ctx context.Context, library, version string) (sessionsync.CrewFetched, error)
}

// Installer installs library versions from a Source.
type Installer struct {
	Src Source
	// Saved, when set, is told about each profile ("agent") and crew ("crew")
	// written, with its canonical bytes, so a running sync does not push it back.
	Saved func(kind, id string, data []byte)
	// Fetched, when set, is told about each profile ("agent") installed, with the
	// exact bytes the library holds for the installed version and the bytes this
	// host now renders for it. They differ when the library copy was written
	// elsewhere (the console), so the host can report the library's hash for a
	// profile that has not been edited since.
	Fetched func(kind, id string, library, rendered []byte)
	// Index, when set, runs after a profile that lists documents is saved and
	// returns a clause for the report.
	Index func(ctx context.Context, name string) string
}

func reason(s string) string { return sanitize.Line(s, 240) }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Profile installs one library profile version. It writes the profile's files
// first, then the profile, then indexes the documents the profile lists, so a
// profile is never saved without the files it names. report is a clause for the
// acknowledgement; why is empty on success.
func (i Installer) Profile(ctx context.Context, library, version string, overwrite bool) (p agentprofile.AgentProfile, report, why string) {
	if !agentprofile.ValidID(library) || !VersionPattern.MatchString(version) {
		return p, "", "that is not a library profile and version"
	}
	md, refs, err := i.Src.Profile(ctx, library, version)
	if err != nil {
		return p, "", "could not read the profile from the library: " + reason(err.Error())
	}
	p, err = agentprofile.ParseMarkdown([]byte(md))
	if err != nil {
		return p, "", "the profile is not valid here: " + reason(err.Error())
	}
	if p.ID != library {
		return p, "", "the profile is not the one the request named"
	}
	if why := Conflict(p, overwrite); why != "" {
		return p, "", why
	}
	skipped := ""
	// A replace keeps the documents and synced files listed on this host when
	// the library copy lists none (a version backed up before the profile had
	// any); a copy that lists them is taken as it is.
	if overwrite {
		if existing, err := agentprofile.Load(p.Name); err == nil {
			if p.Knowledge == nil {
				p.Knowledge = existing.Knowledge
			}
			if sync := existing.SyncPaths(); len(sync) > 0 && len(p.SyncPaths()) == 0 {
				if carried, ok := carrySync(p, existing, sync); ok {
					p = carried
				} else {
					// Keep the library copy as it is rather than refuse a profile
					// that was valid: say what was left behind.
					skipped = "; this host's shared files were not carried over, because the library copy has no worktree to copy them into"
				}
			}
		}
	}
	nfiles, why := i.files(ctx, p, refs)
	if why != "" {
		return p, "", why
	}
	if _, err := agentprofile.Save(p); err != nil {
		return p, "", "could not save the profile: " + reason(err.Error())
	}
	if saved, err := agentprofile.Load(p.Name); err == nil {
		if out, err := agentprofile.MarshalMarkdown(saved); err == nil {
			if i.Saved != nil {
				i.Saved("agent", p.ID, out)
			}
			if i.Fetched != nil {
				i.Fetched("agent", p.ID, []byte(md), out)
			}
		}
	}
	if nfiles > 0 {
		report = fmt.Sprintf(", with %d file%s", nfiles, plural(nfiles, "", "s"))
	}
	report += skipped
	if len(p.KnowledgePaths()) > 0 && i.Index != nil {
		report += "; " + i.Index(ctx, p.Name)
	}
	return p, report, ""
}

// files fetches the files a library version carries and writes them into the
// profile's own directory. A version with no files leaves the directory as it
// is. Nothing is written until every file has been read and checked.
func (i Installer) files(ctx context.Context, p agentprofile.AgentProfile, refs []sessionsync.FileRef) (int, string) {
	if len(refs) == 0 {
		return 0, ""
	}
	if len(refs) > agentfiles.MaxFiles {
		return 0, "the library version lists more files than this host takes"
	}
	files := make([]agentfiles.File, 0, len(refs))
	for _, ref := range refs {
		content, err := i.Src.File(ctx, ref.SHA256)
		if err != nil {
			return 0, "could not read one of the profile's files from the library: " + reason(err.Error())
		}
		files = append(files, agentfiles.File{Path: ref.Path, Content: content, SHA256: ref.SHA256})
	}
	if err := agentfiles.Install(p.ID, files); err != nil {
		return 0, "the profile's files are not ones this host will write: " + reason(err.Error())
	}
	return len(files), ""
}

// Conflict refuses an install that would replace a profile it was not told to.
// A profile of the same name is replaced only with overwrite set and only when
// it has the same id; a profile that holds the same id under another name is
// never replaced.
func Conflict(p agentprofile.AgentProfile, overwrite bool) string {
	if agentprofile.IsBuiltin(p.Name) {
		return "refused: that name is reserved for a built-in profile"
	}
	if holder, ok := agentprofile.ByID(p.ID); ok && holder.Name != p.Name {
		return "refused: this host already has a profile with that id under the name " + sanitize.Line(holder.Name, 64)
	}
	existing, err := agentprofile.Load(p.Name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		return "could not check the existing profile: " + reason(err.Error())
	}
	switch {
	case !overwrite:
		return "this host already has a profile named " + sanitize.Line(p.Name, 64) + "; install it again with replace turned on to overwrite it"
	case existing.ID != p.ID:
		return "refused: the profile of that name here is a different profile (its id differs), so it is not replaced"
	}
	return ""
}

// carrySync puts this host's workspace.sync entries on a library copy that lists
// none, for a replace. Sync needs a worktree to copy into, so a copy that does
// not isolate in one takes the host's worktree isolation when the host has it
// (an agent that shares files is a worktree worker). The result must still be a
// valid profile: when it is not, carrySync reports false and the copy is left as
// it is, so a replace never turns a valid library profile into a refusal.
func carrySync(p, existing agentprofile.AgentProfile, sync []agentprofile.SyncSpec) (agentprofile.AgentProfile, bool) {
	ws := agentprofile.WorkspaceSpec{}
	if p.Workspace != nil {
		ws = *p.Workspace
	}
	ws.Sync = sync
	if (ws.Isolation == "" || ws.Isolation == agentprofile.IsolationNone) &&
		existing.Workspace != nil && existing.Workspace.Isolation == agentprofile.IsolationWorktree {
		ws.Isolation = agentprofile.IsolationWorktree
	}
	candidate := p
	candidate.Workspace = &ws
	if candidate.Validate() != nil {
		return p, false
	}
	return candidate, true
}

// StoredCrew returns the crew of that name saved on this host.
func StoredCrew(name string) (agentprofile.Crew, bool) {
	for _, c := range agentprofile.StoredCrews() {
		if c.Name == name {
			return c, true
		}
	}
	return agentprofile.Crew{}, false
}

// Crew installs one library crew version, members first, or says why not. A
// member this host already has is left alone unless overwrite is set. report is
// the acknowledgement's clause.
func (i Installer) Crew(ctx context.Context, library, version string, overwrite bool) (report, why string) {
	if !agentprofile.ValidID(library) || !VersionPattern.MatchString(version) {
		return "", "that is not a library crew and version"
	}
	got, err := i.Src.Crew(ctx, library, version)
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
	if c.ID != library {
		return "", "the crew is not the one the request named"
	}
	if why := CrewConflict(c, overwrite); why != "" {
		return "", why
	}
	members, why := crewMembers(c, got.Members, overwrite)
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
		_, _, why := i.Profile(ctx, m.ref.Library, m.ref.Version, m.overwrite)
		if why != "" {
			return "", fmt.Sprintf("could not install member %s, so the crew was not written (%d member%s installed first): %s",
				sanitize.Line(m.ref.Profile, 64), len(installed), plural(len(installed), "", "s"), why)
		}
		installed = append(installed, sanitize.Line(m.ref.Profile, 64))
	}
	if _, err := agentprofile.SaveCrew(c); err != nil {
		return "", "could not save the crew: " + reason(err.Error())
	}
	if js, err := c.CanonicalJSON(); err == nil && i.Saved != nil {
		i.Saved("crew", c.ID, js)
	}
	report = fmt.Sprintf("installed crew %s from version %s", sanitize.Line(c.Name, 64), version)
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
func crewMembers(c agentprofile.Crew, refs []sessionsync.CrewMemberRef, overwrite bool) ([]crewMember, string) {
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
		if !agentprofile.ValidID(ref.Library) || !VersionPattern.MatchString(ref.Version) || !listed[ref.Profile] || seen[ref.Profile] {
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

// CrewConflict refuses an install that would replace a crew it was not told to,
// the way Conflict does for a profile: the same name is replaced only with
// overwrite set and only when it has the same id, and a crew that holds the same
// id under another name is never replaced.
func CrewConflict(c agentprofile.Crew, overwrite bool) string {
	if agentprofile.IsBuiltin(c.Name) {
		return "refused: that name is reserved for a built-in crew"
	}
	if holder, ok := agentprofile.CrewByID(c.ID); ok && holder.Name != c.Name {
		return "refused: this host already has a crew with that id under the name " + sanitize.Line(holder.Name, 64)
	}
	existing, ok := StoredCrew(c.Name)
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
