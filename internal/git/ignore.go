package git

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Pattern is one gitignore line, scoped to the directory of the file it came
// from (its domain, as path components below the worktree root).
type Pattern struct {
	domain   []string
	segments []string // the pattern split on "/"
	negate   bool     // "!" prefix: re-include
	dirOnly  bool     // trailing "/"
	anchored bool     // contains a "/" other than a trailing one
}

// ParsePattern parses one gitignore line per gitignore(5). ok is false for
// blank lines and comments. A trailing CR is dropped, as files written on
// Windows carry one.
func ParsePattern(line string, domain []string) (p Pattern, ok bool) {
	line = strings.TrimSuffix(line, "\r")
	if line == "" || strings.HasPrefix(line, "#") {
		return Pattern{}, false
	}
	line = trimTrailingSpaces(line)
	if line == "" {
		return Pattern{}, false
	}
	switch {
	case strings.HasPrefix(line, "!"):
		p.negate = true
		line = line[1:]
	case strings.HasPrefix(line, `\!`), strings.HasPrefix(line, `\#`):
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return Pattern{}, false
	}
	if strings.Contains(line, "/") {
		p.anchored = true
		line = strings.TrimPrefix(line, "/")
	}
	p.segments = strings.Split(line, "/")
	p.domain = append([]string(nil), domain...)
	return p, true
}

// trimTrailingSpaces drops trailing spaces unless escaped with a backslash.
func trimTrailingSpaces(s string) string {
	end := len(s)
	for end > 0 && s[end-1] == ' ' {
		if end >= 2 && s[end-2] == '\\' {
			return s[:end-2] + " " // keep one escaped space, drop the backslash
		}
		end--
	}
	return s[:end]
}

// ReadPatterns parses a gitignore-format file; a missing file has none.
func ReadPatterns(file string, domain []string) ([]Pattern, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ps []Pattern
	for _, line := range strings.Split(string(b), "\n") {
		if p, ok := ParsePattern(line, domain); ok {
			ps = append(ps, p)
		}
	}
	return ps, nil
}

// match reports whether the pattern matches path (components below the
// worktree root).
func (p Pattern) match(pathParts []string, isDir, ignoreCase bool) bool {
	if p.dirOnly && !isDir {
		return false
	}
	if len(pathParts) <= len(p.domain) {
		return false
	}
	for i, d := range p.domain {
		if !equalName(d, pathParts[i], ignoreCase) {
			return false
		}
	}
	rel := pathParts[len(p.domain):]
	if !p.anchored {
		// No slash: the pattern matches the name at any depth.
		return globMatch(p.segments[0], rel[len(rel)-1], ignoreCase)
	}
	// git also matches "x/**" against the directory x itself.
	if n := len(p.segments); isDir && n > 1 && p.segments[n-1] == "**" &&
		matchSegments(p.segments[:n-1], rel, ignoreCase) {
		return true
	}
	return matchSegments(p.segments, rel, ignoreCase)
}

// matchSegments matches pattern segments against path segments, where "**"
// matches zero or more whole directories (a trailing "**" one or more).
func matchSegments(pat, parts []string, ignoreCase bool) bool {
	if len(pat) == 0 {
		return len(parts) == 0
	}
	if pat[0] == "**" {
		if len(pat) == 1 {
			return len(parts) >= 1 // "x/**": everything inside x
		}
		for skip := 0; skip <= len(parts); skip++ {
			if matchSegments(pat[1:], parts[skip:], ignoreCase) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 || !globMatch(pat[0], parts[0], ignoreCase) {
		return false
	}
	return matchSegments(pat[1:], parts[1:], ignoreCase)
}

// globMatch matches one path component: *, ? and [...] classes, with
// git's "[!...]" negation and backslash escapes. A malformed glob never
// matches, as in git.
func globMatch(pattern, name string, ignoreCase bool) bool {
	if ignoreCase {
		pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	}
	pattern = strings.ReplaceAll(pattern, "[!", "[^")
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

func equalName(a, b string, ignoreCase bool) bool {
	if ignoreCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Matcher applies patterns in order of precedence, lowest first; the last
// pattern that matches decides.
type Matcher struct {
	Patterns   []Pattern
	IgnoreCase bool
}

// Ignored reports whether path is ignored by the patterns alone. Callers
// walking a tree must also treat everything below an ignored directory as
// ignored; git does not descend into it.
func (m Matcher) Ignored(pathParts []string, isDir bool) bool {
	for i := len(m.Patterns) - 1; i >= 0; i-- {
		if m.Patterns[i].match(pathParts, isDir, m.IgnoreCase) {
			return !m.Patterns[i].negate
		}
	}
	return false
}
