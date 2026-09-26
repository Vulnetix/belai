package vulnetixcli

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// EnvInfo is the subset of `vulnetix env` relevant to Belai's UI.
type EnvInfo struct {
	APIURL          string
	WebURL          string
	OrgID           string
	GitRemote       string
	GitBranch       string
	PackageManagers []string
	Degraded        []string
}

// getSection returns the first matching section, ignoring case on the keys.
func getSection(secs map[string]string, names ...string) string {
	for n := range secs {
		for _, want := range names {
			if strings.EqualFold(n, want) {
				return secs[n]
			}
		}
	}
	return ""
}

// ParseEnv extracts structured fields from `vulnetix env` output. Every
// section is treated as optional, so the parser degrades cleanly when the CLI
// is run outside a git repository or with partial configuration.
func ParseEnv(text string) EnvInfo {
	text = stripANSI(text)
	secs := sections(text)
	var e EnvInfo

	apiBlock := getSection(secs, "API", "SERVER")
	if api, ok := parseKeyValue(apiBlock, "API URL"); ok {
		e.APIURL = api
	}
	if web, ok := parseKeyValue(getSection(secs, "WEB"), "Web URL"); ok {
		e.WebURL = web
	}
	if org, ok := parseKeyValue(getSection(secs, "AUTH"), "Org ID"); ok {
		e.OrgID = org
	}

	if git := getSection(secs, "GIT", "Git"); git != "" {
		if v, ok := parseKeyValue(git, "Remote"); ok {
			e.GitRemote = v
		}
		if v, ok := parseKeyValue(git, "Branch"); ok {
			e.GitBranch = v
		}
	}

	if pm := getSection(secs, "PACKAGE MANAGERS", "Package Managers"); pm != "" {
		for _, line := range strings.Split(pm, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "-") {
				continue
			}
			if colon := strings.IndexByte(line, ':'); colon > 0 {
				name := strings.TrimSpace(line[:colon])
				status := strings.ToLower(strings.TrimSpace(line[colon+1:]))
				if strings.HasPrefix(status, "installed") || strings.HasPrefix(status, "enabled") || status == "ok" {
					e.PackageManagers = append(e.PackageManagers, name)
				}
			}
		}
	}

	return e
}

// parseKeyValue finds "Key: Value" lines, tolerant of leading markers.
func parseKeyValue(block, key string) (string, bool) {
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Strip a leading [OK]/[FAIL]/[WARN] marker.
		line = markerOK.ReplaceAllString(line, "")
		line = markerFail.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		k := key
		if !strings.HasSuffix(k, ":") {
			k = k + ":"
		}
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(k)) {
			v := strings.TrimSpace(line[len(k):])
			// Some values are wrapped in brackets or quotes; strip simple wrappers.
			v = strings.Trim(v, "[]\"'")
			return v, true
		}
	}
	return "", false
}

// WebURLs are the Vulnetix product URLs resolved from environment and auth state.
type WebURLs struct {
	Root, Register, Dashboard, Pricing, VDB string
}

// ResolveURLs builds validated Vulnetix web URLs. The root comes from
// $VULNETIX_WEB_URL, defaulting to https://www.vulnetix.com. It rejects
// non-http schemes and roots that carry a query or fragment.
func ResolveURLs(getenv func(string) string, authenticated bool) (WebURLs, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	root := getenv("VULNETIX_WEB_URL")
	if root == "" {
		root = "https://www.vulnetix.com"
	}
	u, err := url.Parse(root)
	if err != nil {
		return WebURLs{}, fmt.Errorf("invalid VULNETIX_WEB_URL %q: %w", root, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return WebURLs{}, fmt.Errorf("VULNETIX_WEB_URL scheme %q not allowed", u.Scheme)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return WebURLs{}, fmt.Errorf("VULNETIX_WEB_URL must not contain query or fragment")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}

	w := WebURLs{Root: u.String(), Pricing: u.String() + "pricing", VDB: u.String() + "vdb"}
	if authenticated {
		w.Dashboard = u.ResolveReference(&url.URL{Path: "resolve/dashboard"}).String()
	} else {
		w.Register = u.ResolveReference(&url.URL{Path: "resolve/register"}).String()
	}
	return w, nil
}
