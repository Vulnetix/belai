package vulnetixcli

import (
	"regexp"
	"strings"
)

// Plan is the subscription tier reported by the CLI.
type Plan string

const (
	PlanCommunity  Plan = "community"
	PlanPro        Plan = "pro"
	PlanEnterprise Plan = "enterprise"
	PlanUnknown    Plan = "unknown"
)

// SourceID identifies one credential source.
type SourceID string

const (
	SourceEnvAPIToken  SourceID = "env-api-token"
	SourceEnvAPIKey    SourceID = "env-api-key-org"
	SourceEnvVVD       SourceID = "env-vvd"
	SourceProjectCreds SourceID = "project-credentials"
	SourceHomeCreds    SourceID = "home-credentials"
	SourceKeyring      SourceID = "keyring"
	SourceNetrc        SourceID = "netrc"
	SourceOther        SourceID = "other"
)

// CredentialSource is one authentication method and whether it works.
type CredentialSource struct {
	ID     SourceID `json:"id"`
	Label  string   `json:"label"`
	OK     bool     `json:"ok"`
	Detail string   `json:"detail,omitempty"`
}

// AuthState is the parsed output of `vulnetix auth status`.
type AuthState struct {
	Authenticated bool
	Plan          Plan
	PlanRaw       string
	OrgID         string
	Active        SourceID
	Sources       []CredentialSource
	Firewall      []FirewallEcosystem
	Raw           string
	Parsed        bool
}

// FirewallEcosystem is a placeholder for package-firewall allowlists.
type FirewallEcosystem struct {
	Name string `json:"name"`
}

var planRe = regexp.MustCompile(`(?i)Plan:\s*\[([A-Z]+)\]`)
var uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
var markerLineRe = regexp.MustCompile(`(?i)^\s*\[(OK|FAIL|WARN)\]\s+(.*)$`)

// ParseAuthStatus parses the output of `vulnetix auth status`. It fails closed:
// Authenticated is false unless the output unambiguously says otherwise.
func ParseAuthStatus(text string) AuthState {
	text = stripANSI(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	st := AuthState{Raw: text}

	secs := sections(text)
	authBlock, ok := secs["AUTH STATE"]
	if !ok {
		return st
	}
	st.Parsed = true

	// Authenticated iff an OK/PASS marker exists and no unauthenticated marker.
	lowerAuth := strings.ToLower(authBlock)
	if strings.Contains(lowerAuth, "unauthenticated") {
		st.Authenticated = false
	} else if markerOK.MatchString(authBlock) {
		st.Authenticated = true
	}

	if creds := secs["CREDENTIAL SOURCES"]; creds != "" {
		sources := parseMarkers(creds)
		for _, src := range sources {
			id, label := classifySource(src.Text)
			st.Sources = append(st.Sources, CredentialSource{
				ID:     id,
				Label:  strings.TrimSpace(label),
				OK:     src.OK,
				Detail: strings.TrimSpace(src.Text),
			})
			if src.OK && st.Active == "" {
				st.Active = id
			}
		}
	}

	if m := planRe.FindStringSubmatch(authBlock); m != nil {
		st.PlanRaw = m[1]
		switch strings.ToUpper(m[1]) {
		case "PRO":
			st.Plan = PlanPro
		case "ENTERPRISE":
			st.Plan = PlanEnterprise
		case "COMMUNITY":
			st.Plan = PlanCommunity
		}
	}

	if m := uuidRe.FindString(authBlock); m != "" {
		st.OrgID = m
	}

	// Active is the first OK credential source; SourceOther is a safe fallback
	// if the parser does not recognise the OK line.
	if st.Active == "" && st.Authenticated {
		st.Active = SourceOther
	}

	return st
}

// classifySource maps a prose credential line to a SourceID by keyword.
func classifySource(text string) (SourceID, string) {
	upper := strings.ToUpper(text)
	switch {
	case strings.Contains(upper, "VULNETIX_API_TOKEN"):
		return SourceEnvAPIToken, "VULNETIX_API_TOKEN environment variable"
	case strings.Contains(upper, "VULNETIX_API_KEY"):
		return SourceEnvAPIKey, "VULNETIX_API_KEY environment variable"
	case strings.Contains(upper, "VVD_ORG") || strings.Contains(upper, "VVD_SECRET"):
		return SourceEnvVVD, "VVD_* environment variables"
	case strings.Contains(upper, "PROJECT") && strings.Contains(upper, "CREDENTIAL"):
		return SourceProjectCreds, "project credentials file"
	case strings.Contains(upper, "HOME") && strings.Contains(upper, "CREDENTIAL"):
		return SourceHomeCreds, "home credentials file"
	case strings.Contains(upper, "KEYRING"):
		return SourceKeyring, "system keyring"
	case strings.Contains(upper, "NETRC"):
		return SourceNetrc, ".netrc"
	default:
		return SourceOther, strings.TrimSpace(text)
	}
}
