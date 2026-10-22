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
	d := ewahDecoder{b: b, bitSize: bitSize, words: words}
	for i := 0; i < words; {
		if i, err = d.chunk(i); err != nil {
			return nil, 0, err
		}
	}

	return d.bits, consumed, nil
}

// ewahDecoder is decodeEWAH's state: the input, its declared bit and word
// counts, the bit position of the next word, and the set bits so far.
type ewahDecoder struct {
	b       []byte
	bitSize int
	words   int
	pos     int
	bits    []int
}

// word returns the i'th 64-bit word after the header.
func (d *ewahDecoder) word(i int) uint64 { return binary.BigEndian.Uint64(d.b[8+i*8:]) }

// chunk decodes the run-length word at i and the literal words after it,
// returning the index of the next run-length word.
func (d *ewahDecoder) chunk(i int) (int, error) {
	rlw := d.word(i)
	i++
	runLen := int((rlw >> 1) & 0xffffffff)
	literals := int(rlw >> 33)
	if rlw&1 == 1 {
		if err := d.setRun(runLen); err != nil {
			return 0, err
		}
	}
	d.pos += runLen * 64
	if d.pos > d.bitSize+64*d.words { // a zero run far past the end
		return 0, fmt.Errorf("run extends beyond bitmap size %d", d.bitSize)
	}
	if literals > d.words-i {
		return 0, fmt.Errorf("%d literal words exceed the data", literals)
	}
	for ; literals > 0; literals-- {
		if err := d.literal(d.word(i)); err != nil {
			return 0, err
		}
		i++
	}

	return i, nil
}

// setRun sets the runLen words of ones starting at the current position.
func (d *ewahDecoder) setRun(runLen int) error {
	for k := 0; k < runLen*64; k++ {
		if err := d.add(d.pos + k); err != nil {
			return err
		}
	}

	return nil
}

// literal sets the bits of one literal word and moves past it.
func (d *ewahDecoder) literal(lit uint64) error {
	for bit := 0; bit < 64; bit++ {
		if lit&(1<<bit) != 0 {
			if err := d.add(d.pos + bit); err != nil {
				return err
			}
		}
	}
	d.pos += 64

	return nil
}

func (d *ewahDecoder) add(p int) error {
	if p >= d.bitSize {
		return fmt.Errorf("bit %d beyond bitmap size %d", p, d.bitSize)
	}
	d.bits = append(d.bits, p)

	return nil
}

// applySplitIndex merges idx (the split part) onto its shared index, as git's
// merge_base_index does: drop deleted shared entries, substitute replaced
// ones (a replacement with an empty name keeps the shared entry's name),
// then add the remaining split entries and sort.
func (r *Repository) applySplitIndex(idx *Index) error {
	link := idx.link
	idx.link = nil
	shared, err := r.readSharedIndex(link.base)
	if err != nil {
		return err
	}
	if err := link.check(len(shared.Entries), len(idx.Entries)); err != nil {
		return err
	}
	idx.Entries = mergeSplitIndex(shared.Entries, idx.Entries, link)
	if idx.Tree == nil {
		idx.Tree = shared.Tree
	}

	return nil
}

// readSharedIndex reads and decodes the shared index a split index names.
func (r *Repository) readSharedIndex(base ObjectID) (*Index, error) {
	// The id comes from the index file, but its hex form is one path element.
	name := filepath.Base("sharedindex." + base.String())
	data, err := os.ReadFile(filepath.Clean(filepath.Join(r.GitDir, name)))
	if err != nil {
		return nil, fmt.Errorf("split index: %w", err)
	}
	shared, err := DecodeIndex(data, r.Format)
	if err != nil {
		return nil, fmt.Errorf("split index %s: %w", name, err)
	}
	if shared.link != nil {
		return nil, fmt.Errorf("split index %s: shared index is itself split", name)
	}

	return shared, nil
}

// check verifies the bitmaps against the shared index's sharedLen entries
// and the split index's splitLen entries.
func (l *indexLink) check(sharedLen, splitLen int) error {
	for _, p := range append(append([]int(nil), l.deleted...), l.replaced...) {
		if p >= sharedLen {
			return fmt.Errorf("split index: position %d beyond %d shared entries", p, sharedLen)
		}
	}
	if len(l.replaced) > splitLen {
		return fmt.Errorf("split index: %d replacements but %d entries", len(l.replaced), splitLen)
	}

	return nil
}

// mergeSplitIndex applies split onto base, as link describes, and returns
// the entries sorted by name and stage. base is modified.
func mergeSplitIndex(base, split []IndexEntry, link *indexLink) []IndexEntry {
	deleted := make(map[int]bool, len(link.deleted))
	for _, p := range link.deleted {
		deleted[p] = true
	}
	for i, p := range link.replaced {
		e := split[i]
		if e.Name == "" {
			e.Name = base[p].Name
		}
		base[p] = e
	}
	type key struct {
		name  string
		stage int
	}
	merged := make(map[key]IndexEntry, len(base)+len(split))
	for i, e := range base {
		if !deleted[i] {
			merged[key{e.Name, e.Stage}] = e
		}
	}
	for _, e := range split[len(link.replaced):] {
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

	return entries
}
