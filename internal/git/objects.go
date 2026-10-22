package git

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Tree entry modes, after git's canonicalization.
const (
	ModeTree       uint32 = 0o040000
	ModeRegular    uint32 = 0o100644
	ModeExecutable uint32 = 0o100755
	ModeSymlink    uint32 = 0o120000
	ModeGitlink    uint32 = 0o160000
)

// maxTagDepth bounds a chain of tags pointing at tags.
const maxTagDepth = 64

// Commit holds the commit fields git-dirty uses.
type Commit struct {
	Tree       ObjectID
	Parents    []ObjectID
	CommitTime time.Time
}

// ParseCommit parses a commit object's headers. Multi-line headers such as
// gpgsig and mergetag (continuation lines start with a space) are skipped.
func ParseCommit(data []byte, f Format) (*Commit, error) {
	c := &Commit{}
	haveTree, haveCommitter := false, false
	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte("\n"))
		data = rest
		if len(line) == 0 {
			break // end of headers; the message follows
		}
		if line[0] == ' ' {
			continue // continuation of a multi-line header
		}
		key, value, _ := bytes.Cut(line, []byte(" "))
		switch string(key) {
		case "tree":
			if haveTree {
				return nil, errors.New("commit: duplicate tree header")
			}
			id, err := f.ParseHex(string(value))
			if err != nil {
				return nil, fmt.Errorf("commit tree: %w", err)
			}
			c.Tree, haveTree = id, true
		case "parent":
			id, err := f.ParseHex(string(value))
			if err != nil {
				return nil, fmt.Errorf("commit parent: %w", err)
			}
			c.Parents = append(c.Parents, id)
		case "committer":
			t, err := parseSignatureTime(value)
			if err != nil {
				return nil, fmt.Errorf("commit committer: %w", err)
			}
			c.CommitTime, haveCommitter = t, true
		}
	}
	if !haveTree {
		return nil, errors.New("commit: missing tree header")
	}
	if !haveCommitter {
		return nil, errors.New("commit: missing committer header")
	}
	return c, nil
}

// parseSignatureTime reads "<name> <<email>> <unix seconds> <+hhmm>".
func parseSignatureTime(sig []byte) (time.Time, error) {
	gt := bytes.LastIndexByte(sig, '>')
	if gt < 0 {
		return time.Time{}, errors.New("malformed signature")
	}
	fields := bytes.Fields(sig[gt+1:])
	if len(fields) < 1 {
		return time.Time{}, errors.New("signature has no timestamp")
	}
	secs, err := strconv.ParseInt(string(fields[0]), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("signature timestamp: %w", err)
	}
	loc := time.UTC
	if len(fields) > 1 && len(fields[1]) == 5 && (fields[1][0] == '+' || fields[1][0] == '-') {
		hh, err1 := strconv.Atoi(string(fields[1][1:3]))
		mm, err2 := strconv.Atoi(string(fields[1][3:5]))
		if err1 == nil && err2 == nil {
			off := (hh*60 + mm) * 60
			if fields[1][0] == '-' {
				off = -off
			}
			loc = time.FixedZone(string(fields[1]), off)
		}
	}
	return time.Unix(secs, 0).In(loc), nil
}

// Tag holds the target of an annotated tag.
type Tag struct {
	Object ObjectID
	Type   ObjectType
}

// ParseTag parses an annotated tag's object and type headers.
func ParseTag(data []byte, f Format) (*Tag, error) {
	t := &Tag{}
	haveObject := false
	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte("\n"))
		data = rest
		if len(line) == 0 {
			break
		}
		key, value, _ := bytes.Cut(line, []byte(" "))
		switch string(key) {
		case "object":
			id, err := f.ParseHex(string(value))
			if err != nil {
				return nil, fmt.Errorf("tag object: %w", err)
			}
			t.Object, haveObject = id, true
		case "type":
			typ, ok := parseObjectType(string(value))
			if !ok {
				return nil, fmt.Errorf("tag type %q", value)
			}
			t.Type = typ
		}
	}
	if !haveObject || t.Type == 0 {
		return nil, errors.New("tag: missing object or type header")
	}
	return t, nil
}

// TreeEntry is one entry of a tree object.
type TreeEntry struct {
	Mode uint32 // canonical: ModeTree, ModeRegular, ModeExecutable, ModeSymlink or ModeGitlink
	Name string
	ID   ObjectID
}

// ParseTree parses a tree object: entries of "<octal mode> <name>\0<id>".
func ParseTree(data []byte, f Format) ([]TreeEntry, error) {
	var entries []TreeEntry
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		if sp <= 0 {
			return nil, errors.New("tree entry: missing mode")
		}
		mode, err := strconv.ParseUint(string(data[:sp]), 8, 32)
		if err != nil {
			return nil, fmt.Errorf("tree entry mode %q", data[:sp])
		}
		data = data[sp+1:]
		nul := bytes.IndexByte(data, 0)
		if nul <= 0 {
			return nil, errors.New("tree entry: missing name")
		}
		name := string(data[:nul])
		data = data[nul+1:]
		if len(data) < f.Size {
			return nil, errors.New("tree entry: truncated id")
		}
		id, _ := f.IDFromBytes(data[:f.Size])
		data = data[f.Size:]
		entries = append(entries, TreeEntry{Mode: canonMode(uint32(mode)), Name: name, ID: id})
	}
	return entries, nil
}

// canonMode maps a tree entry mode to git's canonical form (canon_mode):
// any regular file becomes 100644 or 100755 by its executable bit, so old
// trees with modes such as 100664 compare equal to the index.
func canonMode(m uint32) uint32 {
	switch m & 0o170000 {
	case 0o100000:
		if m&0o100 != 0 {
			return ModeExecutable
		}
		return ModeRegular
	case 0o120000:
		return ModeSymlink
	case 0o040000:
		return ModeTree
	}
	return ModeGitlink
}

// ReadCommit reads and parses a commit.
func (r *Repository) ReadCommit(id ObjectID) (*Commit, error) {
	t, data, err := r.ReadObject(id)
	if err != nil {
		return nil, err
	}
	if t != ObjCommit {
		return nil, fmt.Errorf("object %s is a %s, not a commit", id, t)
	}
	c, err := ParseCommit(data, r.Format)
	if err != nil {
		return nil, fmt.Errorf("object %s: %w", id, err)
	}
	return c, nil
}

// Peel follows annotated tags from id to the object they finally name.
func (r *Repository) Peel(id ObjectID) (ObjectID, error) {
	for range maxTagDepth {
		t, data, err := r.ReadObject(id)
		if err != nil {
			return ObjectID{}, err
		}
		if t != ObjTag {
			return id, nil
		}
		tag, err := ParseTag(data, r.Format)
		if err != nil {
			return ObjectID{}, fmt.Errorf("object %s: %w", id, err)
		}
		id = tag.Object
	}
	return ObjectID{}, fmt.Errorf("tag chain deeper than %d", maxTagDepth)
}

// WalkTree calls fn for every non-tree entry below the tree id, with paths
// joined by "/", in tree order. Gitlinks are reported, not descended.
func (r *Repository) WalkTree(id ObjectID, fn func(path string, e TreeEntry) error) error {
	return r.walkTree(id, "", fn, 0)
}

// maxTreeDepth bounds tree recursion; a hostile tree could otherwise nest
// (or loop) without end.
const maxTreeDepth = 4096

func (r *Repository) walkTree(id ObjectID, prefix string, fn func(string, TreeEntry) error, depth int) error {
	if depth > maxTreeDepth {
		return fmt.Errorf("tree nesting deeper than %d", maxTreeDepth)
	}
	t, data, err := r.ReadObject(id)
	if err != nil {
		return err
	}
	if t != ObjTree {
		return fmt.Errorf("object %s is a %s, not a tree", id, t)
	}
	entries, err := ParseTree(data, r.Format)
	if err != nil {
		return fmt.Errorf("object %s: %w", id, err)
	}
	for _, e := range entries {
		path := prefix + e.Name
		if e.Mode == ModeTree {
			if err := r.walkTree(e.ID, path+"/", fn, depth+1); err != nil {
				return err
			}
			continue
		}
		if err := fn(path, e); err != nil {
			return err
		}
	}
	return nil
}
