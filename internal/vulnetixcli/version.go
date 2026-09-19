package vulnetixcli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a parsed Vulnetix CLI version.
type Version struct {
	Major, Minor, Patch int
	Pre, Raw            string
}

// String returns the normalised version string.
func (v Version) String() string {
	if v.Major == 0 && v.Minor == 0 && v.Patch == 0 && v.Raw == "" {
		return ""
	}
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		return s + "-" + v.Pre
	}
	return s
}

// IsZero reports whether no version components were parsed.
func (v Version) IsZero() bool {
	return v.Major == 0 && v.Minor == 0 && v.Patch == 0 && v.Pre == "" && v.Raw == ""
}

// Compare returns -1, 0, or 1 relative to another version. Pre-release
// versions sort before the release with the same base version.
func (v Version) Compare(o Version) int {
	for _, pair := range []struct{ a, b int }{
		{v.Major, o.Major},
		{v.Minor, o.Minor},
		{v.Patch, o.Patch},
	} {
		if pair.a < pair.b {
			return -1
		}
		if pair.a > pair.b {
			return 1
		}
	}
	switch {
	case v.Pre == "" && o.Pre != "":
		return 1
	case v.Pre != "" && o.Pre == "":
		return -1
	case v.Pre != o.Pre:
		if v.Pre < o.Pre {
			return -1
		}
		return 1
	}
	return 0
}

var versionRe = regexp.MustCompile(`(?i)v?v?(\d+)\.(\d+)\.(\d+)(?:[-.]?(.+))?`)

// ParseVersion extracts a Version from a string. It accepts leading "vv",
// a single "v", or no prefix. The bool reports whether a version was found.
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Version{}, false
	}
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{Raw: s}, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	pre := strings.TrimPrefix(m[4], "-")
	pre = strings.TrimPrefix(pre, ".")
	return Version{Major: major, Minor: minor, Patch: patch, Pre: pre, Raw: s}, true
}
