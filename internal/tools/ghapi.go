package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// graphqlMutationRE matches a GraphQL document that begins with a mutation
// operation, ignoring leading whitespace. It is the closed allowlist form for
// the GH graphql subcommand.
var graphqlMutationRE = regexp.MustCompile(`(?i)^\s*mutation\b`)

// ghAPIGate is the second gate for the GH native tool. The first gate,
// cloudAllowed, confirms the command begins with an allowed prefix; this
// function inspects the full token stream for gh api calls and rejects
// anything that is not a provable GET against an allowed path family.
//
// It runs after the shell-metacharacter gate, so it never sees shell syntax.
func ghAPIGate(cmd string) error {
	fields := strings.Fields(cmd)
	if len(fields) == 0 || fields[0] != "api" {
		return nil
	}
	if len(fields) == 1 {
		return fmt.Errorf("gh api requires a path")
	}

	// graphql is special: it is the only gh api form that requires -f, so
	// the generic field refusal below cannot apply. Instead, inspect the
	// query text and reject mutations.
	if fields[1] == "graphql" {
		for i := 1; i < len(fields)-1; i++ {
			k := fields[i]
			if k != "-f" && k != "-F" && k != "--field" && k != "--raw-field" {
				continue
			}
			v := fields[i+1]
			query, ok := parseQueryField(v)
			if !ok {
				continue
			}
			if graphqlMutationRE.MatchString(query) {
				return fmt.Errorf("graphql mutations are not allowed")
			}
		}
		return nil
	}

	var path string
	hi := 1
	for hi < len(fields) {
		tok := fields[hi]
		if !strings.HasPrefix(tok, "-") {
			if path != "" {
				return fmt.Errorf("gh api accepts exactly one path, got multiple non-flag tokens")
			}
			path = tok
			hi++
			continue
		}

		// Value-consuming read flags: skip the value.
		switch tok {
		case "-X", "--method":
			if hi+1 >= len(fields) {
				return fmt.Errorf("flag %s requires a value", tok)
			}
			method := strings.ToLower(fields[hi+1])
			if method != "get" && method != "head" {
				return fmt.Errorf("only GET and HEAD methods are allowed for gh api, got %q", method)
			}
			hi += 2
			continue
		case "--method=":
			return fmt.Errorf("--method= is not a valid flag form")
		case "-H", "--header":
		case "--hostname":
		case "--cache":
		case "-q", "--jq":
		case "-t", "--template":
		default:
			// Could be --method=GET style or valueless flags.
			if strings.HasPrefix(tok, "--method=") {
				method := strings.ToLower(strings.TrimPrefix(tok, "--method="))
				if method != "get" && method != "head" {
					return fmt.Errorf("only GET and HEAD methods are allowed for gh api, got %q", method)
				}
				hi++
				continue
			}
			// These valueless flags are allowed.
			if tok == "--paginate" || tok == "-i" || tok == "--include" || tok == "--silent" || tok == "--verbose" || tok == "--slurp" {
				hi++
				continue
			}
			// Anything else is unknown: fail closed.
			return fmt.Errorf("unrecognised gh api flag: %s", tok)
		}
		// For value-consuming read flags (the switch cases above that fell
		// through), consume the value and continue.
		if hi+1 >= len(fields) {
			return fmt.Errorf("flag %s requires a value", tok)
		}
		hi += 2
	}

	if path == "" {
		return fmt.Errorf("gh api requires a path")
	}

	gp, _, _ := strings.Cut(path, "?")
	gp = strings.TrimPrefix(gp, "/")
	allowed := []string{
		"repos/", "orgs/", "users/", "user", "search/", "rate_limit", "meta", "gitignore/", "licenses/",
	}
	for _, prefix := range allowed {
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(gp, prefix) {
				return nil
			}
			continue
		}
		if gp == prefix || strings.HasPrefix(gp, prefix+"/") {
			return nil
		}
	}
	return fmt.Errorf("gh api path %q is not in the read-only allowlist", path)
}

// parseQueryField extracts the query= value from a graphql -f argument. Shell
// quotes around the value are stripped so a quoted mutation document is still
// inspected.
func parseQueryField(v string) (string, bool) {
	key, val, ok := strings.Cut(v, "=")
	if !ok {
		return "", false
	}
	if strings.TrimSpace(key) != "query" {
		return "", false
	}
	val = strings.TrimSpace(val)
	if len(val) >= 2 {
		if (val[0] == '\'' && val[len(val)-1] == '\'') || (val[0] == '"' && val[len(val)-1] == '"') {
			val = val[1 : len(val)-1]
		}
	}
	return val, true
}
