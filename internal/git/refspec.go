package git

import (
	"fmt"
	"strings"
)

// RefSpec is a fetch refspec: [+]src:dst, at most one "*" on each side, or
// a negative "^src" that excludes matching refs.
type RefSpec struct {
	src, dst string
	negative bool
}

// ParseRefSpec parses one remote.<name>.fetch value.
func ParseRefSpec(s string) (RefSpec, error) {
	s = strings.TrimPrefix(s, "+")
	if rest, ok := strings.CutPrefix(s, "^"); ok {
		if strings.Contains(rest, ":") || strings.Count(rest, "*") > 1 {
			return RefSpec{}, fmt.Errorf("refspec %q: invalid negative refspec", s)
		}

		return RefSpec{src: rest, negative: true}, nil
	}
	src, dst, ok := strings.Cut(s, ":")
	if !ok || src == "" {
		return RefSpec{}, fmt.Errorf("refspec %q: missing src:dst", s)
	}
	if strings.Count(src, "*") > 1 || strings.Count(src, "*") != strings.Count(dst, "*") {
		return RefSpec{}, fmt.Errorf("refspec %q: mismatched wildcards", s)
	}

	return RefSpec{src: src, dst: dst}, nil
}

// match reports whether name matches the source side, returning what "*"
// stood for.
func (s RefSpec) match(name string) (string, bool) {
	prefix, suffix, glob := strings.Cut(s.src, "*")
	if !glob {
		return "", name == s.src
	}
	if len(name) < len(prefix)+len(suffix) || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", false
	}

	return name[len(prefix) : len(name)-len(suffix)], true
}

// MapRef maps a remote ref name through fetch refspecs to the local ref that
// holds it, as git does for @{upstream}: the first positive refspec that
// matches wins, unless a negative refspec matches.
func MapRef(specs []RefSpec, name string) (string, bool) {
	for _, s := range specs {
		if _, ok := s.match(name); ok && s.negative {
			return "", false
		}
	}
	for _, s := range specs {
		if s.negative {
			continue
		}
		if star, ok := s.match(name); ok {
			if s.dst == "" {
				return "", false
			}

			return strings.Replace(s.dst, "*", star, 1), true
		}
	}

	return "", false
}
