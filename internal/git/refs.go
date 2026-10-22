package git

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrRefNotFound is returned for a reference that does not exist.
var ErrRefNotFound = errors.New("reference not found")

// symrefMaxDepth is git's SYMREF_MAXDEPTH.
const symrefMaxDepth = 5

// Ref is one reference as stored: symbolic (Target set) or direct (ID set).
// Peeled is the commit an annotated tag points at, when packed-refs records
// it.
type Ref struct {
	Name   string
	Target string
	ID     ObjectID
	Peeled ObjectID
}

type packedRef struct {
	id     ObjectID
	peeled ObjectID
}

// ValidRefName reports whether name is HEAD or a well-formed refs/... name,
// following git check-ref-format. Names reach the filesystem as paths, and
// some come from repository config a hostile clone controls, so anything
// that could escape refs/ is rejected.
func ValidRefName(name string) bool {
	if name == "HEAD" {
		return true
	}
	if !strings.HasPrefix(name, "refs/") || strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".") || strings.Contains(name, "..") ||
		strings.Contains(name, "@{") || strings.Contains(name, "//") {
		return false
	}
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c < 0x20 || c == 0x7f:
			return false
		case strings.IndexByte(" ~^:?*[\\", c) >= 0:
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// refPath returns where a loose ref lives: HEAD and per-worktree refs in the
// git dir, everything else in the common dir.
func (r *Repository) refPath(name string) string {
	dir := r.CommonDir
	if name == "HEAD" || strings.HasPrefix(name, "refs/bisect/") ||
		strings.HasPrefix(name, "refs/worktree/") || strings.HasPrefix(name, "refs/rewritten/") {
		dir = r.GitDir
	}
	return filepath.Join(dir, filepath.FromSlash(name))
}

// ReadRef reads one reference without following symbolic refs: a loose file
// first, then packed-refs.
func (r *Repository) ReadRef(name string) (Ref, error) {
	if !ValidRefName(name) {
		return Ref{}, fmt.Errorf("invalid reference name %q", name)
	}
	b, err := os.ReadFile(r.refPath(name))
	switch {
	case err == nil:
		return r.parseLooseRef(name, b)
	case !errors.Is(err, fs.ErrNotExist) && !isDirErr(err):
		return Ref{}, err
	}
	if name == "HEAD" {
		return Ref{}, fmt.Errorf("HEAD: %w", ErrRefNotFound)
	}
	packed, err := r.packedRefs()
	if err != nil {
		return Ref{}, err
	}
	if p, ok := packed[name]; ok {
		return Ref{Name: name, ID: p.id, Peeled: p.peeled}, nil
	}
	return Ref{}, fmt.Errorf("%s: %w", name, ErrRefNotFound)
}

// isDirErr reports a read of a directory, as for "refs/heads/a" when
// "refs/heads/a/b" exists; git treats that ref as absent.
func isDirErr(err error) bool {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		fi, statErr := os.Stat(pe.Path)
		return statErr == nil && fi.IsDir()
	}
	return false
}

func (r *Repository) parseLooseRef(name string, b []byte) (Ref, error) {
	s := strings.TrimRight(string(b), " \t\r\n")
	if target, ok := strings.CutPrefix(s, "ref:"); ok {
		target = strings.TrimSpace(target)
		if !ValidRefName(target) {
			return Ref{}, fmt.Errorf("%s: invalid symbolic target %q", name, target)
		}
		return Ref{Name: name, Target: target}, nil
	}
	id, err := r.Format.ParseHex(s)
	if err != nil {
		return Ref{}, fmt.Errorf("%s: invalid content: %w", name, err)
	}
	return Ref{Name: name, ID: id}, nil
}

// Resolve follows symbolic refs from name to an object id.
func (r *Repository) Resolve(name string) (ObjectID, error) {
	for range symrefMaxDepth + 1 {
		ref, err := r.ReadRef(name)
		if err != nil {
			return ObjectID{}, err
		}
		if ref.Target == "" {
			return ref.ID, nil
		}
		name = ref.Target
	}
	return ObjectID{}, fmt.Errorf("%s: symbolic reference chain deeper than %d", name, symrefMaxDepth)
}

// packedRefs loads packed-refs once. A tag's peeled id is recorded when the
// file says so ("peeled" or "fully-peeled" traits and "^<id>" lines).
func (r *Repository) packedRefs() (map[string]packedRef, error) {
	if r.packedRead {
		return r.packed, nil
	}
	refs := map[string]packedRef{}
	f, err := os.Open(filepath.Join(r.CommonDir, "packed-refs"))
	if errors.Is(err, fs.ErrNotExist) {
		r.packed, r.packedRead = refs, true
		return refs, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	last := ""
	for line := 1; sc.Scan(); line++ {
		text := bytes.TrimRight(sc.Bytes(), "\r")
		switch {
		case len(text) == 0 || text[0] == '#':
			continue
		case text[0] == '^':
			id, err := r.Format.ParseHex(string(text[1:]))
			if err != nil || last == "" {
				return nil, fmt.Errorf("packed-refs line %d: bad peeled line", line)
			}
			p := refs[last]
			p.peeled = id
			refs[last] = p
			last = ""
		default:
			hexID, name, ok := strings.Cut(string(text), " ")
			id, err := r.Format.ParseHex(hexID)
			if !ok || err != nil || !ValidRefName(name) {
				return nil, fmt.Errorf("packed-refs line %d: malformed", line)
			}
			refs[name] = packedRef{id: id}
			last = name
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	r.packed, r.packedRead = refs, true
	return refs, nil
}

// Tags returns every refs/tags/ reference, loose and packed, sorted by name.
// A loose tag overrides a packed one of the same name.
func (r *Repository) Tags() ([]Ref, error) {
	packed, err := r.packedRefs()
	if err != nil {
		return nil, err
	}
	byName := map[string]Ref{}
	for name, p := range packed {
		if strings.HasPrefix(name, "refs/tags/") {
			byName[name] = Ref{Name: name, ID: p.id, Peeled: p.peeled}
		}
	}
	root := filepath.Join(r.CommonDir, "refs", "tags")
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(r.CommonDir, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if !ValidRefName(name) {
			return nil // a lock file or other non-ref file
		}
		ref, err := r.ReadRef(name)
		if err != nil {
			return err
		}
		byName[name] = ref
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Ref, 0, len(byName))
	for _, ref := range byName {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
