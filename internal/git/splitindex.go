package git

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// indexLink is the split-index "link" extension: the shared index the
// entries are based on, and which of its entries are deleted or replaced.
type indexLink struct {
	base     ObjectID
	deleted  []int // shared-index positions, ascending
	replaced []int
}

func parseLink(b []byte, f Format) (*indexLink, error) {
	if len(b) < f.Size {
		return nil, errors.New("link extension: truncated")
	}
	l := &indexLink{}
	l.base, _ = f.IDFromBytes(b[:f.Size])
	rest := b[f.Size:]
	if len(rest) == 0 {
		return l, nil // no bitmaps: nothing deleted or replaced
	}
	var n1, n2 int
	var err error
	if l.deleted, n1, err = decodeEWAH(rest); err != nil {
		return nil, fmt.Errorf("link extension: delete bitmap: %w", err)
	}
	if l.replaced, n2, err = decodeEWAH(rest[n1:]); err != nil {
		return nil, fmt.Errorf("link extension: replace bitmap: %w", err)
	}
	if n1+n2 != len(rest) {
		return nil, errors.New("link extension: trailing bytes after the bitmaps")
	}
	return l, nil
}

// maxEWAHBits caps a bitmap's declared size; a split index never has more
// entries than this, and the cap bounds what a hostile bitmap can expand to.
const maxEWAHBits = 1 << 24

// decodeEWAH decodes git's EWAH bitmap serialization (ewah/ewah_io.c): bit
// size, word count, 64-bit big-endian words, and the last run-length word's
// position. It returns the set bit positions and the bytes consumed. Runs
// of zeros are skipped without expanding, and every set bit must fall below
// the declared bit size, so a hostile bitmap cannot force a large result.
func decodeEWAH(b []byte) (bits []int, consumed int, err error) {
	if len(b) < 12 {
		return nil, 0, errors.New("truncated header")
	}
	bitSize := int(binary.BigEndian.Uint32(b[0:4]))
	words := int(binary.BigEndian.Uint32(b[4:8]))
	if bitSize > maxEWAHBits {
		return nil, 0, fmt.Errorf("bitmap size %d too large", bitSize)
	}
	if words > (len(b)-12)/8 {
		return nil, 0, fmt.Errorf("%d words exceed the data", words)
	}
	consumed = 8 + words*8 + 4
	w := func(i int) uint64 { return binary.BigEndian.Uint64(b[8+i*8:]) }

	pos := 0 // bit position of the next word
	add := func(p int) error {
		if p >= bitSize {
			return fmt.Errorf("bit %d beyond bitmap size %d", p, bitSize)
		}
		bits = append(bits, p)
		return nil
	}
	for i := 0; i < words; {
		rlw := w(i)
		i++
		runBit := rlw&1 == 1
		runLen := int((rlw >> 1) & 0xffffffff)
		literals := int(rlw >> 33)
		if runBit {
			for k := 0; k < runLen*64; k++ {
				if err := add(pos + k); err != nil {
					return nil, 0, err
				}
			}
		}
		pos += runLen * 64
		if pos > bitSize+64*words { // a zero run far past the end
			return nil, 0, fmt.Errorf("run extends beyond bitmap size %d", bitSize)
		}
		if literals > words-i {
			return nil, 0, fmt.Errorf("%d literal words exceed the data", literals)
		}
		for ; literals > 0; literals-- {
			lit := w(i)
			i++
			for bit := 0; bit < 64; bit++ {
				if lit&(1<<bit) != 0 {
					if err := add(pos + bit); err != nil {
						return nil, 0, err
					}
				}
			}
			pos += 64
		}
	}
	return bits, consumed, nil
}

// applySplitIndex merges idx (the split part) onto its shared index, as git's
// merge_base_index does: drop deleted shared entries, substitute replaced
// ones (a replacement with an empty name keeps the shared entry's name),
// then add the remaining split entries and sort.
func (r *Repository) applySplitIndex(idx *Index) error {
	link := idx.link
	idx.link = nil
	name := "sharedindex." + link.base.String() // hex only: never a path
	data, err := os.ReadFile(filepath.Join(r.GitDir, name))
	if err != nil {
		return fmt.Errorf("split index: %w", err)
	}
	shared, err := DecodeIndex(data, r.Format)
	if err != nil {
		return fmt.Errorf("split index %s: %w", name, err)
	}
	if shared.link != nil {
		return fmt.Errorf("split index %s: shared index is itself split", name)
	}
	n := len(shared.Entries)
	for _, p := range append(append([]int(nil), link.deleted...), link.replaced...) {
		if p >= n {
			return fmt.Errorf("split index: position %d beyond %d shared entries", p, n)
		}
	}
	if len(link.replaced) > len(idx.Entries) {
		return fmt.Errorf("split index: %d replacements but %d entries", len(link.replaced), len(idx.Entries))
	}

	deleted := make(map[int]bool, len(link.deleted))
	for _, p := range link.deleted {
		deleted[p] = true
	}
	base := shared.Entries
	for i, p := range link.replaced {
		e := idx.Entries[i]
		if e.Name == "" {
			e.Name = base[p].Name
		}
		base[p] = e
	}
	type key struct {
		name  string
		stage int
	}
	merged := make(map[key]IndexEntry, n+len(idx.Entries))
	for i, e := range base {
		if !deleted[i] {
			merged[key{e.Name, e.Stage}] = e
		}
	}
	for _, e := range idx.Entries[len(link.replaced):] {
		merged[key{e.Name, e.Stage}] = e
	}
	entries := make([]IndexEntry, 0, len(merged))
	for _, e := range merged {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Stage < entries[j].Stage
	})
	idx.Entries = entries
	if idx.Tree == nil {
		idx.Tree = shared.Tree
	}
	return nil
}
