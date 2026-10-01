package rc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentfiles"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The agent library. Two requests from the website reach a profile on this
// host, both through the dispatch queue and both carrying identifiers only:
//
//	profile_backup   export the named profile as markdown and upload it, with
//	                 the files it names, so the website keeps a copy that
//	                 outlives this machine
//	profile_install  read one version of a library profile and its files and
//	                 write them here, then index the documents it lists
//
// A backup writes nothing on this host. An install is a person's action on the
// website, made with their own login, so it is not held to the rules a web
// prompt is: it does not need sync.remote_prompts and a profile may be as
// permissive as the user chooses. It is still checked for being a profile at all:
// the markdown is parsed strictly and validated whole, as `belai agent import`
// does, and it never replaces a profile it was not told to. The refusal text sent
// back is harness words with a short cleaned excerpt, never the profile.
//
// The files a profile names (knowledge.paths and workspace.sync) travel with it
// (internal/agentfiles). A backup captures them under the harness's fixed
// floor: no symlinks, no credentials, no key or token, bounded text. An install
// writes them only into the profile's own directory under the state directory,
// from where the profile's paths resolve when this host has no file of that name,
// and never into a repository.

// versionPattern is a library version: the UTC minute it was saved.
var versionPattern = regexp.MustCompile(`^\d{12}$`)

// indexTimeout bounds the indexing an install runs.
const indexTimeout = 45 * time.Second

// reason reduces text that may hold a fragment of the website's profile (a tool
// name, a YAML error) to one clean short line for the acknowledgement.
func reason(s string) string { return sanitize.Line(s, 240) }

// roots are the directories a relative path in a profile is looked up under: the
// directories this daemon offers.
func (d *Daemon) roots() []string {
	out := make([]string, 0, len(d.o.Dirs))
	for _, dir := range d.o.Dirs {
		out = append(out, dir.Path)
	}
	return out
}

// backupProfile exports the profile the request names and uploads it with its
// files. It returns the report for the acknowledgement, or the reason it refused.
func (d *Daemon) backupProfile(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	name := strings.TrimSpace(r.Profile)
	if name == "" || len(name) > 128 || sanitize.Line(name, 128) != name {
		return "", "that is not a profile name"
	}
	if agentprofile.IsBuiltin(name) {
		return "", "built-in profiles ship with Belai, so there is nothing to back up"
	}
	p, err := agentprofile.Load(name)
	if err != nil {
		return "", "this host has no profile " + sanitize.Line(name, 64)
	}
	if p.ID == "" {
		// A profile file dropped in by hand has no id yet. Give it one so the
		// library can tell it from any other.
		if _, err := agentprofile.EnsureIDs(); err != nil {
			return "", "could not give the profile an id: " + reason(err.Error())
		}
		if p, err = agentprofile.Load(name); err != nil || p.ID == "" {
			return "", "could not give the profile an id"
		}
	}
	md, err := agentprofile.MarshalMarkdown(p)
	if err != nil {
		return "", "could not export the profile: " + reason(err.Error())
	}
	refs, note := d.uploadFiles(ctx, r.ID, p)
	version, err := d.o.Client.LibraryBackupFiles(ctx, d.o.HostID, r.ID, string(md), refs)
	if err != nil {
		return "", "the library did not take the profile: " + reason(err.Error())
	}
	d.markSynced("agent", p.ID, md)
	return fmt.Sprintf("backed up %s as version %s%s", sanitize.Line(name, 64), sanitize.Ident(version, 12), note), ""
}

// uploadFiles captures the files the profile names and uploads them for the
// request. It returns the manifest to send with the profile, and a note for the
// acknowledgement. A nil manifest says nothing about the files, which keeps the
// library's latest copy: that is what a host with none of the files, or a server
// that cannot take them, sends, so a backup never empties what another host
// saved. An empty manifest clears them, which only a profile that names none
// does.
func (d *Daemon) uploadFiles(ctx context.Context, dispatch string, p agentprofile.AgentProfile) ([]sessionsync.FileRef, string) {
	listed := agentfiles.ListedPaths(p)
	if len(listed) == 0 {
		return []sessionsync.FileRef{}, ""
	}
	files, rep, err := agentfiles.Capture(ctx, p, d.roots())
	if err != nil {
		return nil, "; its files were not captured (" + reason(err.Error()) + ")"
	}
	if len(files) == 0 {
		if rep.Total() == 0 {
			return nil, ""
		}
		return nil, "; no file could be read here (" + skippedNote(rep) + ")"
	}
	refs := make([]sessionsync.FileRef, 0, len(files))
	for _, f := range files {
		if err := d.o.Client.LibraryUploadFile(ctx, d.o.HostID, dispatch, f.SHA256, f.Content); err != nil {
			// An old server has no file routes; keep the profile backup.
			return nil, "; its files were not uploaded (" + reason(err.Error()) + ")"
		}
		refs = append(refs, sessionsync.FileRef{Path: f.Path, SHA256: f.SHA256})
	}
	note := fmt.Sprintf(" with %d file%s", len(refs), plural(len(refs), "", "s"))
	if rep.Total() > 0 {
		note += ", leaving out " + skippedNote(rep)
	}
	return refs, note
}

// skippedNote says what a capture left out, by count and reason only.
func skippedNote(r agentfiles.Report) string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(r.Missing, "not found here")
	add(r.Secrets, "holding a key or token")
	add(r.Refused, "never read")
	add(r.OverLimit, "over the limits")
	return strings.Join(parts, ", ")
}

// installProfile reads the library version the request names and writes it as
// a profile on this host, or says why it did not.
func (d *Daemon) installProfile(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	p, report, why := d.installOne(ctx, r.ID, r.Library, r.Version, r.Overwrite)
	if why != "" {
		return "", why
	}
	verb := "installed"
	if r.Overwrite {
		verb = "installed (replacing any profile of that name and id)"
	}
	return fmt.Sprintf("%s %s from version %s%s", verb, sanitize.Line(p.Name, 64), r.Version, report), ""
}

// installOne installs one library profile version, answering the request
// dispatchID. It writes the profile's files first, then the profile, then
// indexes the documents the profile lists, so a profile is never saved without
// the files it names. report is a clause for the acknowledgement.
func (d *Daemon) installOne(ctx context.Context, dispatchID, library, version string, overwrite bool) (p agentprofile.AgentProfile, report, why string) {
	if !agentprofile.ValidID(library) || !versionPattern.MatchString(version) {
		return p, "", "that is not a library profile and version"
	}
	md, refs, err := d.o.Client.LibraryFetchFiles(ctx, d.o.HostID, library, version, dispatchID)
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
	if why := installConflict(p, overwrite); why != "" {
		return p, "", why
	}
	// A replace keeps the documents and synced files listed on this host when
	// the library copy lists none (a version backed up before the profile had
	// any); a copy that lists them is taken as it is.
	if overwrite {
		if existing, err := agentprofile.Load(p.Name); err == nil {
			if p.Knowledge == nil {
				p.Knowledge = existing.Knowledge
			}
			if sync := existing.SyncPaths(); len(sync) > 0 && len(p.SyncPaths()) == 0 {
				ws := agentprofile.WorkspaceSpec{}
				if p.Workspace != nil {
					ws = *p.Workspace
				}
				ws.Sync = sync
				p.Workspace = &ws
			}
		}
	}
	nfiles, why := d.installFiles(ctx, dispatchID, p, refs)
	if why != "" {
		return p, "", why
	}
	if _, err := agentprofile.Save(p); err != nil {
		return p, "", "could not save the profile: " + reason(err.Error())
	}
	if saved, err := agentprofile.Load(p.Name); err == nil {
		if out, err := agentprofile.MarshalMarkdown(saved); err == nil {
			d.markSynced("agent", p.ID, out)
		}
	}
	if nfiles > 0 {
		report = fmt.Sprintf(", with %d file%s", nfiles, plural(nfiles, "", "s"))
	}
	if len(p.KnowledgePaths()) > 0 {
		report += "; " + d.indexProfile(ctx, p.Name)
	}
	return p, report, ""
}

// installFiles fetches the files a library version carries and writes them into
// the profile's own directory. A version with no files leaves the directory as
// it is. Nothing is written until every file has been read and checked.
func (d *Daemon) installFiles(ctx context.Context, dispatchID string, p agentprofile.AgentProfile, refs []sessionsync.FileRef) (int, string) {
	if len(refs) == 0 {
		return 0, ""
	}
	if len(refs) > agentfiles.MaxFiles {
		return 0, "the library version lists more files than this host takes"
	}
	files := make([]agentfiles.File, 0, len(refs))
	for _, ref := range refs {
		content, err := d.o.Client.LibraryFetchFile(ctx, d.o.HostID, ref.SHA256, dispatchID)
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

// indexProfile indexes the documents a freshly installed profile lists, through
// the same classifier `belai agent knowledge -index` uses, run from a directory
// this daemon offers (the index reads the project's own files beside the
// profile's, and the security classifier needs a trusted workspace). It is best
// effort: the agent indexes on its first run anyway. The clause it returns says
// what happened, never what the documents hold.
func (d *Daemon) indexProfile(ctx context.Context, name string) string {
	dir := ""
	for _, offered := range d.o.Dirs {
		if offered.Source == SourceTrusted {
			dir = offered.Path
			break
		}
	}
	if dir == "" {
		return "its documents are indexed the first time the agent runs (no trusted directory is offered to index from)"
	}
	ictx, cancel := context.WithTimeout(ctx, indexTimeout)
	defer cancel()
	out, err := d.o.Index(ictx, d.o.Exe, dir, name)
	if err != nil {
		if ictx.Err() != nil {
			return "its documents are still being indexed and finish the first time the agent runs"
		}
		return "could not index its documents yet (" + reason(err.Error()) + ")"
	}
	return "indexed its documents" + out
}

// installConflict refuses an install that would replace a profile it was not
// told to. A profile of the same name is replaced only with overwrite set and
// only when it has the same id; a profile that holds the same id under another
// name is never replaced.
func installConflict(p agentprofile.AgentProfile, overwrite bool) string {
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

// remotePromptsOn reports whether this host takes requests that write from the
// website. It reads the user's own settings and fails closed.
func remotePromptsOn() bool {
	s, err := config.LoadGlobal()
	return err == nil && s.SyncRemotePromptsEnabled()
}

// syncProfilesOn reports whether this host keeps the website's library current
// by itself (sync.profiles). It reads the user's own settings and fails closed.
func syncProfilesOn() bool {
	s, err := config.LoadGlobal()
	return err == nil && s.SyncProfilesEnabled()
}
