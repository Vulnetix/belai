package rc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The agent library. Two requests from the website reach a profile on this
// host, both through the dispatch queue and both carrying identifiers only:
//
//	profile_backup   export the named profile as markdown and upload it, so the
//	                 website keeps a copy that outlives this machine
//	profile_install  read one version of a library profile and write it here
//
// A backup writes nothing on this host. An install is a prompt-grade write from
// the website, so it fails closed: it needs sync.remote_prompts, the profile is
// parsed strictly and validated whole, it may not relax a safety setting, and it
// never replaces a profile it was not told to. The refusal text sent back is
// harness words with a short cleaned excerpt, never the profile.

// versionPattern is a library version: the UTC minute it was saved.
var versionPattern = regexp.MustCompile(`^\d{12}$`)

// reason reduces text that may hold a fragment of the website's profile (a tool
// name, a YAML error) to one clean short line for the acknowledgement.
func reason(s string) string { return sanitize.Line(s, 240) }

// backupProfile exports the profile the request names and uploads it. It
// returns the report for the acknowledgement, or the reason it refused.
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
	version, err := d.o.Client.LibraryBackup(ctx, d.o.HostID, r.ID, string(md))
	if err != nil {
		return "", "the library did not take the profile: " + reason(err.Error())
	}
	return fmt.Sprintf("backed up %s as version %s", sanitize.Line(name, 64), sanitize.Ident(version, 12)), ""
}

// installProfile reads the library version the request names and writes it as
// a profile on this host, or says why it did not.
func (d *Daemon) installProfile(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	if !d.o.RemotePrompts() {
		return "", "sync.remote_prompts is off on this host, so the website cannot install a profile"
	}
	if !agentprofile.ValidID(r.Library) || !versionPattern.MatchString(r.Version) {
		return "", "that is not a library profile and version"
	}
	md, err := d.o.Client.LibraryFetch(ctx, d.o.HostID, r.Library, r.Version, r.ID)
	if err != nil {
		return "", "could not read the profile from the library: " + reason(err.Error())
	}
	p, err := agentprofile.ParseMarkdown([]byte(md))
	if err != nil {
		return "", "the profile is not valid here: " + reason(err.Error())
	}
	if p.ID != r.Library {
		return "", "the profile is not the one the request named"
	}
	if why := profileRelaxation(p); why != "" {
		return "", why
	}
	if why := installConflict(p, r.Overwrite); why != "" {
		return "", why
	}
	if _, err := agentprofile.Save(p); err != nil {
		return "", "could not save the profile: " + reason(err.Error())
	}
	verb := "installed"
	if r.Overwrite {
		verb = "installed (replacing any profile of that name and id)"
	}
	return fmt.Sprintf("%s %s from version %s", verb, sanitize.Line(p.Name, 64), r.Version), ""
}

// profileRelaxation names the safety setting a web-installed profile may not
// relax, or returns "" when it relaxes none. The user can still make such a
// profile by hand on this host, with the checks Validate applies; the website
// cannot.
func profileRelaxation(p agentprofile.AgentProfile) string {
	switch {
	case p.Guardrails != nil && !*p.Guardrails:
		return "refused: the profile turns guardrails off, which the website cannot install"
	case p.AskPermission != nil && !*p.AskPermission:
		return "refused: the profile turns permission asks off, which the website cannot install"
	case p.Autonomy == agentprofile.AutonomyAutonomous:
		return "refused: the profile is autonomous, which the website cannot install"
	case p.HasTool("Bash"):
		// An empty tools list is the full default registry, Bash included.
		return "refused: the profile allows Bash. List its tools without Bash (an empty list allows every tool)"
	}
	for _, t := range p.Tools {
		switch t {
		case "BashOutput", "KillShell", "ProcessRestart":
			return "refused: the profile lists a shell process tool, which the website cannot install"
		}
	}
	return ""
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
