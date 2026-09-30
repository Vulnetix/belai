package scanartifacts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// commitRE is a full git object id. Only this shape counts as a commit, so a
// string an artefact carries can never stand in for one.
var commitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// maxCommitProbe bounds how much of one artefact is read to find its commit.
const maxCommitProbe = DefaultMaxBytes

// Evidence names the artefact files that record a review of one commit.
type Evidence struct {
	Commit string
	Files  []string // names relative to the artefact directory, sorted
}

// ReviewedAt reports whether any artefact under dir (a `.vulnetix`
// directory) records a scan of head, by equality of the full commit id.
// Sources are `memory.yaml` (`last_scan.git_commit`), a CycloneDX file's
// `vulnetix:git/commit` property and a SARIF file's `runs[].properties.git.commit`.
// There is no clock, cache or freshness rule: the commit either matches or
// it does not. A head that is not a full commit id never matches.
func ReviewedAt(dir, head string) (bool, Evidence) {
	head = strings.ToLower(strings.TrimSpace(head))
	ev := Evidence{Commit: head}
	if !commitRE.MatchString(head) {
		return false, ev
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, ev
	}
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		path := filepath.Join(dir, name)
		var got string
		switch {
		case name == "memory.yaml":
			if m, err := ParseMemorySummary(path); err == nil {
				got = m.GitCommit
			}
		case strings.HasSuffix(name, ".cdx.json"):
			got = cdxCommit(path)
		case strings.HasSuffix(name, ".sarif"):
			got = sarifCommit(path)
		}
		if strings.ToLower(got) == head {
			ev.Files = append(ev.Files, name)
		}
	}
	sort.Strings(ev.Files)
	return len(ev.Files) > 0, ev
}

func readProbe(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := budgetReadAll(f, maxCommitProbe)
	if err != nil {
		return nil
	}
	return data
}

func cdxCommit(path string) string {
	data := readProbe(path)
	if data == nil {
		return ""
	}
	var doc struct {
		Metadata struct {
			Component struct {
				Properties []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"properties"`
			} `json:"component"`
		} `json:"metadata"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	for _, p := range doc.Metadata.Component.Properties {
		if p.Name == "vulnetix:git/commit" {
			return p.Value
		}
	}
	return ""
}

func sarifCommit(path string) string {
	data := readProbe(path)
	if data == nil {
		return ""
	}
	var doc struct {
		Runs []struct {
			Properties struct {
				Git struct {
					Commit string `json:"commit"`
				} `json:"git"`
			} `json:"properties"`
		} `json:"runs"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	for _, r := range doc.Runs {
		if r.Properties.Git.Commit != "" {
			return r.Properties.Git.Commit
		}
	}
	return ""
}
