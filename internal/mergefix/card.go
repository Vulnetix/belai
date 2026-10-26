package mergefix

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Card is the pull request a merge-conflict card names, read from the fenced
// block of harness-readable lines at the top of its body:
//
//	pr: https://github.com/owner/name/pull/N
//	repo: owner/name
//	number: N
//	head: <head ref>
//	base: <base ref>
//	fork: true|false
type Card struct {
	PR     string
	Repo   string
	Number int
	Head   string
	Base   string
	Fork   bool
}

var (
	cardRepoRE = regexp.MustCompile(`^[a-z0-9_.][a-z0-9_.-]{0,99}/[a-z0-9_.][a-z0-9_.-]{0,99}$`)
	cardRefRE  = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	cardPRRE   = regexp.MustCompile(`^https://([a-z0-9.-]+)/([^/\s]+)/([^/\s]+)/pull/([0-9]{1,9})$`)
)

// ValidRef reports whether ref is a branch name the card may carry: plain
// characters, no option, no dot-dot, no empty or dotted segment and no lock
// suffix. git's own check-ref-format is run as well before the ref is used.
func ValidRef(ref string) bool {
	if !cardRefRE.MatchString(ref) || strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, ".lock") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, seg := range strings.Split(ref, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return true
}

// ParseCard reads the card's pull request block. Every key is required, each
// once, and each value must have its shape; the pull request link must name
// the same repository and number.
func ParseCard(body string) (Card, error) {
	block, ok := fencedBlock(body)
	if !ok {
		return Card{}, errors.New("the card has no fenced block naming its pull request")
	}
	vals := map[string]string{}
	for _, l := range strings.Split(block, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return Card{}, errors.New("the pull request block has a line that is not key: value")
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "pr", "repo", "number", "head", "base", "fork":
		default:
			continue
		}
		if _, dup := vals[k]; dup {
			return Card{}, fmt.Errorf("the pull request block names %s twice", k)
		}
		vals[k] = v
	}
	var missing []string
	for _, k := range []string{"pr", "repo", "number", "head", "base", "fork"} {
		if _, ok := vals[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Card{}, fmt.Errorf("the pull request block lacks %s", strings.Join(missing, ", "))
	}
	c := Card{PR: vals["pr"], Repo: vals["repo"], Head: vals["head"], Base: vals["base"]}
	if !cardRepoRE.MatchString(c.Repo) {
		return Card{}, errors.New("repo is not a lower-case owner/name")
	}
	n, err := strconv.Atoi(vals["number"])
	if err != nil || n <= 0 {
		return Card{}, errors.New("number is not a pull request number")
	}
	c.Number = n
	if !ValidRef(c.Head) || !ValidRef(c.Base) {
		return Card{}, errors.New("head or base is not a plain branch name")
	}
	if c.Head == c.Base {
		return Card{}, errors.New("head and base are the same branch")
	}
	switch vals["fork"] {
	case "true":
		c.Fork = true
	case "false":
	default:
		return Card{}, errors.New("fork is not true or false")
	}
	m := cardPRRE.FindStringSubmatch(c.PR)
	if m == nil {
		return Card{}, errors.New("pr is not a pull request link")
	}
	if strings.ToLower(m[2]+"/"+m[3]) != c.Repo || m[4] != strconv.Itoa(c.Number) {
		return Card{}, errors.New("pr names another repository or number than repo and number")
	}
	return c, nil
}

// Host is the forge host the pull request link names.
func (c Card) Host() string {
	if m := cardPRRE.FindStringSubmatch(c.PR); m != nil {
		return m[1]
	}
	return ""
}

// fencedBlock returns the text of the first ``` fenced block.
func fencedBlock(body string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			if start < 0 {
				start = i
				continue
			}
			return strings.Join(lines[start+1:i], "\n"), true
		}
	}
	return "", false
}

// Implicated returns the conflicted files a check's output names: a path from
// files that appears in output as a location (followed by a colon or an
// opening parenthesis). Only paths from files are returned, so nothing of the
// output itself is: the output decides membership and nothing else.
func Implicated(output string, files []string) []string {
	var out []string
	for _, f := range files {
		if f != "" && locates(output, f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// locates reports whether output names path as a location: the path, at the
// start of a word or after "./", followed by a colon or an opening
// parenthesis. "pkg/a.go:3" names pkg/a.go and not a.go.
func locates(output, path string) bool {
	for from := 0; ; {
		i := strings.Index(output[from:], path)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(path)
		before := i == 0 || strings.ContainsRune(" \t\n\"'(", rune(output[i-1])) || (i >= 2 && output[i-2:i] == "./" && (i == 2 || strings.ContainsRune(" \t\n\"'(", rune(output[i-3]))))
		after := end < len(output) && (output[end] == ':' || output[end] == '(')
		if before && after {
			return true
		}
		from = i + 1
	}
}
