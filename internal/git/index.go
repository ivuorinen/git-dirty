package git

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Index is a decoded .git/index.
type Index struct {
	Version uint32
	Entries []IndexEntry
	Tree    *CacheTree // root cache-tree entry; nil when absent
	ModTime time.Time  // the index file's mtime, for the racy-git check
	link    *indexLink // split-index link extension; see splitindex.go
}

// IndexEntry is one staged path.
type IndexEntry struct {
	CTime, MTime                   time.Time
	Dev, Ino, Mode, UID, GID, Size uint32
	ID                             ObjectID
	Stage                          int
	Name                           string
	AssumeValid                    bool
	SkipWorktree                   bool
	IntentToAdd                    bool
}

// CacheTree is the root of the TREE extension: the tree the index would
// write, valid when Entries >= 0.
type CacheTree struct {
	Entries  int
	Subtrees int
	ID       ObjectID
}

// Entry flag bits, on disk.
const (
	flagAssumeValid   = 0x8000
	flagExtended      = 0x4000
	flagStageMask     = 0x3000
	flagNameMask      = 0x0fff
	extSkipWorktree   = 0x4000
	extIntentToAdd    = 0x2000
	statFieldsSize    = 40 // ctime, mtime (sec+nsec), dev, ino, mode, uid, gid, size
	indexHeaderSize   = 12
	minIndexEntrySize = statFieldsSize + 20 + 2
)

// ReadIndex reads this worktree's index. A missing file is an empty index.
func (r *Repository) ReadIndex() (*Index, error) {
	path := filepath.Join(r.GitDir, "index")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Index{Version: 2}, nil
	}
	if err != nil {
		return nil, err
	}
	// Errors are not prefixed: decoder messages already name the index, and
	// callers add their own context.
	idx, err := DecodeIndex(data, r.Format)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil {
		idx.ModTime = fi.ModTime()
	}
	if idx.link != nil {
		if err := r.applySplitIndex(idx); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

// DecodeIndex parses index file content. The trailing checksum is not
// verified (git's index.skipHash writes zeros there).
func DecodeIndex(data []byte, f Format) (*Index, error) {
	if len(data) < indexHeaderSize+f.Size || string(data[:4]) != "DIRC" {
		return nil, errors.New("not an index file")
	}
	idx := &Index{Version: binary.BigEndian.Uint32(data[4:8])}
	if idx.Version < 2 || idx.Version > 4 {
		return nil, fmt.Errorf("unsupported index version %d", idx.Version)
	}
	count := binary.BigEndian.Uint32(data[8:12])
	end := len(data) - f.Size // start of the trailing checksum
	if uint64(count) > uint64(end)/minIndexEntrySize {
		return nil, fmt.Errorf("entry count %d exceeds the file size", count)
	}
	idx.Entries = make([]IndexEntry, 0, count)
	d := indexDecoder{data: data[:end], pos: indexHeaderSize, f: f, version: idx.Version}
	for range count {
		e, err := d.entry()
		if err != nil {
			return nil, err
		}
		idx.Entries = append(idx.Entries, e)
	}
	if err := d.extensions(idx); err != nil {
		return nil, err
	}
	return idx, nil
}

type indexDecoder struct {
	data     []byte
	pos      int
	f        Format
	version  uint32
	prevName []byte // version 4 path compression base
}

func (d *indexDecoder) need(n int) error {
	if n < 0 || d.pos+n > len(d.data) {
		return fmt.Errorf("index truncated at byte %d", d.pos)
	}
	return nil
}

func (d *indexDecoder) u32() uint32 {
	v := binary.BigEndian.Uint32(d.data[d.pos:])
	d.pos += 4
	return v
}

func (d *indexDecoder) entry() (IndexEntry, error) {
	start := d.pos
	if err := d.need(statFieldsSize + d.f.Size + 2); err != nil {
		return IndexEntry{}, err
	}
	var e IndexEntry
	cs, cn := d.u32(), d.u32()
	ms, mn := d.u32(), d.u32()
	e.CTime = time.Unix(int64(cs), int64(cn))
	e.MTime = time.Unix(int64(ms), int64(mn))
	e.Dev, e.Ino, e.Mode, e.UID, e.GID, e.Size = d.u32(), d.u32(), d.u32(), d.u32(), d.u32(), d.u32()
	e.ID, _ = d.f.IDFromBytes(d.data[d.pos : d.pos+d.f.Size])
	d.pos += d.f.Size
	flags := binary.BigEndian.Uint16(d.data[d.pos:])
	d.pos += 2
	e.AssumeValid = flags&flagAssumeValid != 0
	e.Stage = int(flags&flagStageMask) >> 12
	if flags&flagExtended != 0 {
		if d.version < 3 {
			return IndexEntry{}, errors.New("extended flags in a version 2 index")
		}
		if err := d.need(2); err != nil {
			return IndexEntry{}, err
		}
		ext := binary.BigEndian.Uint16(d.data[d.pos:])
		d.pos += 2
		e.SkipWorktree = ext&extSkipWorktree != 0
		e.IntentToAdd = ext&extIntentToAdd != 0
	}
	fixed := d.pos - start

	if d.version == 4 {
		strip, err := d.varint()
		if err != nil {
			return IndexEntry{}, err
		}
		if strip > uint64(len(d.prevName)) {
			return IndexEntry{}, fmt.Errorf("index v4 entry strips %d of %d bytes", strip, len(d.prevName))
		}
		nul := bytes.IndexByte(d.data[d.pos:], 0)
		if nul < 0 {
			return IndexEntry{}, errors.New("index v4 entry name not terminated")
		}
		name := append(d.prevName[:len(d.prevName)-int(strip):len(d.prevName)-int(strip)], d.data[d.pos:d.pos+nul]...)
		d.pos += nul + 1
		d.prevName = name
		e.Name = string(name)
		return e, nil
	}

	nameLen := int(flags & flagNameMask)
	if nameLen == flagNameMask { // long name: read up to the NUL
		nul := bytes.IndexByte(d.data[d.pos:], 0)
		if nul < 0 {
			return IndexEntry{}, errors.New("index entry name not terminated")
		}
		nameLen = nul
	}
	if err := d.need(nameLen + 1); err != nil {
		return IndexEntry{}, err
	}
	if d.data[d.pos+nameLen] != 0 {
		return IndexEntry{}, errors.New("index entry name length mismatch")
	}
	e.Name = string(d.data[d.pos : d.pos+nameLen])
	// Entries are NUL-padded to a multiple of 8 bytes.
	next := start + (fixed+nameLen+8)&^7
	if next > len(d.data) {
		return IndexEntry{}, fmt.Errorf("index truncated at byte %d", d.pos)
	}
	d.pos = next
	return e, nil
}

// varint decodes git's offset varint (index v4 strip count): each
// continuation adds one before shifting, so encodings are unique.
func (d *indexDecoder) varint() (uint64, error) {
	var v uint64
	for i := 0; ; i++ {
		if d.pos >= len(d.data) || i > 9 {
			return 0, errors.New("index v4 varint truncated or too long")
		}
		c := d.data[d.pos]
		d.pos++
		v = v<<7 | uint64(c&0x7f)
		if c&0x80 == 0 {
			return v, nil
		}
		v++
	}
}

// extensions reads the extension blocks between the entries and the
// checksum: TREE and link are decoded, other optional extensions (uppercase
// signature) skipped, and any other mandatory one refused, as git does.
func (d *indexDecoder) extensions(idx *Index) error {
	for d.pos < len(d.data) {
		if err := d.need(8); err != nil {
			return err
		}
		sig := string(d.data[d.pos : d.pos+4])
		d.pos += 4
		size := int(d.u32())
		if err := d.need(size); err != nil {
			return fmt.Errorf("extension %q: %w", sig, err)
		}
		body := d.data[d.pos : d.pos+size]
		d.pos += size
		switch {
		case sig == "TREE":
			t, err := parseRootCacheTree(body, d.f)
			if err != nil {
				return err
			}
			idx.Tree = t
		case sig == "link":
			l, err := parseLink(body, d.f)
			if err != nil {
				return err
			}
			idx.link = l
		case sig[0] >= 'A' && sig[0] <= 'Z':
			// optional: REUC, UNTR, FSMN, EOIE, IEOT, ...
		default:
			return fmt.Errorf("unsupported index extension %q", sig)
		}
	}
	return nil
}

// parseRootCacheTree decodes the first TREE record, which is the root.
func parseRootCacheTree(b []byte, f Format) (*CacheTree, error) {
	nul := bytes.IndexByte(b, 0)
	if nul != 0 { // the root's path is empty
		return nil, errors.New("TREE extension: missing root entry")
	}
	b = b[1:]
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return nil, errors.New("TREE extension: truncated")
	}
	countStr, subStr, ok := bytes.Cut(b[:nl], []byte(" "))
	if !ok {
		return nil, errors.New("TREE extension: malformed counts")
	}
	entries, err1 := strconv.Atoi(string(countStr))
	subtrees, err2 := strconv.Atoi(string(subStr))
	if err1 != nil || err2 != nil || subtrees < 0 || entries < -1 {
		return nil, errors.New("TREE extension: malformed counts")
	}
	t := &CacheTree{Entries: entries, Subtrees: subtrees}
	if entries >= 0 {
		rest := b[nl+1:]
		if len(rest) < f.Size {
			return nil, errors.New("TREE extension: truncated id")
		}
		t.ID, _ = f.IDFromBytes(rest[:f.Size])
	}
	return t, nil
}
