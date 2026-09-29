package locate

import (
	"os"
	"path"
	"regexp"
	"strings"
)

// A small gitignore matcher: enough of the format for the inventory to agree
// with git about what a checkout contains. Supported: blank lines and #
// comments, ! negation, a trailing / for directories only, a leading / or an
// inner / to anchor to the file's directory, *, ?, [..] classes and **.
// Not supported: escapes of a leading # or !, and attributes. A pattern that
// does not compile is dropped, which can only include more files, never leak
// an ignored one through a syntax error in the matcher itself.

type ignoreRule struct {
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
}

// ignoreFile is the rules of one .gitignore or .ignore, relative to its
// directory.
type ignoreFile struct {
	// dir is the directory of the file, relative to the inventory root ("" for
	// the root).
	dir   string
	rules []ignoreRule
	// git is true for a .gitignore, which a nested repository resets; a
	// .ignore is never reset.
	git bool
}

func parseIgnore(data []byte, dir string, git bool) *ignoreFile {
	f := &ignoreFile{dir: dir, git: git}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimRight(line, " ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimRight(line, "/")
		}
		if line == "" {
			continue
		}
		anchored := strings.Contains(line, "/")
		line = strings.TrimPrefix(line, "/")
		re, err := regexp.Compile(globRegexp(line, anchored))
		if err != nil {
			continue
		}
		r.re = re
		f.rules = append(f.rules, r)
	}
	return f
}

// globRegexp turns a gitignore glob into a regular expression over a path
// relative to the ignore file's directory.
func globRegexp(glob string, anchored bool) string {
	var b strings.Builder
	if anchored {
		b.WriteString("^")
	} else {
		b.WriteString("(?:^|.*/)")
	}
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				// "**/" matches zero or more directories; a trailing "**"
				// matches everything below.
				if i+2 < len(glob) && glob[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := strings.IndexByte(glob[i:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := glob[i+1 : i+j]
			class = strings.Replace(class, "!", "^", 1)
			b.WriteString("[" + class + "]")
			i += j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("(?:/.*)?$")
	return b.String()
}

// match reports the verdict of this file's rules for rel (relative to the
// inventory root): ignored, and whether any rule matched at all.
func (f *ignoreFile) match(rel string, isDir bool) (ignored, matched bool) {
	sub := rel
	if f.dir != "" {
		var ok bool
		sub, ok = strings.CutPrefix(rel, f.dir+"/")
		if !ok {
			return false, false
		}
	}
	for _, r := range f.rules {
		if r.dirOnly && !isDir && !dirPrefixMatch(r, sub) {
			continue
		}
		if r.re.MatchString(sub) {
			ignored, matched = !r.negate, true
		}
	}
	return ignored, matched
}

// dirPrefixMatch reports whether a directory-only rule matches an ancestor of
// the file at sub, which is how a file under an ignored directory is ignored
// even though the walk normally never enters that directory.
func dirPrefixMatch(r ignoreRule, sub string) bool {
	for p := path.Dir(sub); p != "." && p != "/"; p = path.Dir(p) {
		if r.re.MatchString(p) {
			return true
		}
	}
	return false
}

// readIgnore loads a .gitignore or .ignore from dir (an absolute directory).
func readIgnore(abs, rel, name string, git bool) *ignoreFile {
	data, err := os.ReadFile(abs + "/" + name)
	if err != nil || len(data) > 1<<20 {
		return nil
	}
	return parseIgnore(data, rel, git)
}
