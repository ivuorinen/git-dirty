package git

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// pack is one packfile and its version 2 .idx. The .idx is searched with
// ReadAt instead of being loaded: only the 1 KiB fanout table is kept in
// memory, so opening a repository with large packs stays cheap.
type pack struct {
	name     string // base path without extension
	f        Format
	idx      *os.File
	idxSize  int64
	count    uint32
	fanout   [256]uint32
	data     *os.File // opened on first object read
	dataSize int64
}

const (
	idxHeaderSize = 8 // "\377tOc" + version
	idxFanoutSize = 256 * 4
)

func openPack(idxPath string, f Format) (*pack, error) {
	file, err := os.Open(filepath.Clean(idxPath))
	if err != nil {
		return nil, err
	}
	p := &pack{name: strings.TrimSuffix(idxPath, ".idx"), f: f, idx: file}
	if err := p.readIdxHeader(); err != nil {
		_ = file.Close() // read-only; the header error is the one to report

		return nil, fmt.Errorf("%s: %w", idxPath, err)
	}

	return p, nil
}

func (p *pack) readIdxHeader() error {
	fi, err := p.idx.Stat()
	if err != nil {
		return err
	}
	p.idxSize = fi.Size()
	var hdr [idxHeaderSize + idxFanoutSize]byte
	if _, err := p.idx.ReadAt(hdr[:], 0); err != nil {
		return fmt.Errorf("pack index header: %w", err)
	}
	if string(hdr[:4]) != "\377tOc" || binary.BigEndian.Uint32(hdr[4:8]) != 2 {
		return errors.New("unsupported pack index (want version 2)")
	}
	var prev uint32
	for i := range 256 {
		v := binary.BigEndian.Uint32(hdr[idxHeaderSize+i*4:])
		if v < prev {
			return errors.New("pack index fanout is not monotonic")
		}
		p.fanout[i], prev = v, v
	}
	p.count = p.fanout[255]
	// Names, CRCs and 4-byte offsets, then two trailing checksums.
	minSize := int64(idxHeaderSize+idxFanoutSize) + int64(p.count)*int64(p.f.Size+8) + 2*int64(p.f.Size)
	if p.idxSize < minSize {
		return fmt.Errorf("pack index too small for %d objects", p.count)
	}

	return nil
}

func (p *pack) namesOffset() int64 { return idxHeaderSize + idxFanoutSize }
func (p *pack) offsetsOffset() int64 {
	return p.namesOffset() + int64(p.count)*int64(p.f.Size+4) // names, then CRCs
}

// find returns the pack offset of id.
func (p *pack) find(id ObjectID) (int64, bool, error) {
	key := id.b[:id.n]
	if len(key) != p.f.Size {
		return 0, false, nil
	}
	lo, hi := uint32(0), p.fanout[key[0]]
	if key[0] > 0 {
		lo = p.fanout[key[0]-1]
	}
	buf := make([]byte, p.f.Size)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if _, err := p.idx.ReadAt(buf, p.namesOffset()+int64(mid)*int64(p.f.Size)); err != nil {
			return 0, false, err
		}
		switch c := bytes.Compare(buf, key); {
		case c == 0:
			off, err := p.offset(mid)

			return off, err == nil, err
		case c < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}

	return 0, false, nil
}

// offset reads entry i's pack offset, following the 8-byte large-offset
// table when the high bit of the 4-byte offset is set.
func (p *pack) offset(i uint32) (int64, error) {
	var b [8]byte
	if _, err := p.idx.ReadAt(b[:4], p.offsetsOffset()+int64(i)*4); err != nil {
		return 0, err
	}
	off := binary.BigEndian.Uint32(b[:4])
	if off&0x80000000 == 0 {
		return int64(off), nil
	}
	at := p.offsetsOffset() + int64(p.count)*4 + int64(off&0x7fffffff)*8
	if at+8 > p.idxSize-2*int64(p.f.Size) {
		return 0, errors.New("pack index large offset out of range")
	}
	if _, err := p.idx.ReadAt(b[:], at); err != nil {
		return 0, err
	}
	big := binary.BigEndian.Uint64(b[:])
	if big > 1<<62 {
		return 0, errors.New("pack index large offset out of range")
	}

	return int64(big), nil
}

// openData opens the .pack file and checks its header.
func (p *pack) openData() error {
	if p.data != nil {
		return nil
	}
	file, err := os.Open(p.name + ".pack")
	if err != nil {
		return err
	}
	size, err := p.checkData(file)
	if err != nil {
		_ = file.Close() // read-only; the header error is the one to report

		return err
	}
	p.data, p.dataSize = file, size

	return nil
}

// checkData checks a .pack file's header and returns the file's size.
func (p *pack) checkData(file *os.File) (int64, error) {
	fi, err := file.Stat()
	if err != nil {
		return 0, err
	}
	var hdr [12]byte
	if _, err := file.ReadAt(hdr[:], 0); err != nil {
		return 0, fmt.Errorf("%s.pack: header: %w", p.name, err)
	}
	if v := binary.BigEndian.Uint32(hdr[4:8]); string(hdr[:4]) != "PACK" || (v != 2 && v != 3) {
		return 0, fmt.Errorf("%s.pack: not a version 2 or 3 pack", p.name)
	}

	return fi.Size(), nil
}

func (p *pack) close() error {
	err := p.idx.Close()
	if p.data != nil {
		if e := p.data.Close(); err == nil {
			err = e
		}
	}

	return err
}

// packEntry is a parsed entry header.
type packEntry struct {
	off     int64 // the entry's own offset, the cache key
	typ     ObjectType
	size    uint64 // inflated size: the object, or the delta for delta types
	dataOff int64  // start of the zlib stream
	baseOff int64  // OFS_DELTA base
	baseID  ObjectID
}

// entry parses the entry header at off.
func (p *pack) entry(off int64) (packEntry, error) {
	b, err := p.entryHeader(off)
	if err != nil {
		return packEntry{}, err
	}
	r := entryReader{b: b, off: off}
	e, err := r.typeAndSize()
	if err != nil {
		return packEntry{}, err
	}
	switch e.typ {
	case ObjCommit, ObjTree, ObjBlob, ObjTag:
	case objOfsDelta:
		e.baseOff, err = r.ofsDeltaBase()
	case objRefDelta:
		e.baseID, err = r.refDeltaBase(p.f)
	default:
		err = fmt.Errorf("pack entry at %d has invalid type %d", off, e.typ)
	}
	if err != nil {
		return packEntry{}, err
	}
	if e.size > maxObjectSize {
		return packEntry{}, fmt.Errorf("pack entry at %d declares %d bytes, over the limit", off, e.size)
	}
	e.dataOff = off + int64(r.pos)

	return e, nil
}

// entryHeader reads the bytes at off that can hold an entry header: up to
// its size varint and a REF_DELTA base id, less at the end of the pack.
func (p *pack) entryHeader(off int64) ([]byte, error) {
	if err := p.openData(); err != nil {
		return nil, err
	}
	if off < 12 || off >= p.dataSize {
		return nil, fmt.Errorf("pack offset %d out of range", off)
	}
	var buf [16 + maxHashSize]byte
	n, err := p.data.ReadAt(buf[:], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	return buf[:n], nil
}

// entryReader reads a pack entry header from the bytes at offset off.
type entryReader struct {
	b   []byte
	pos int
	off int64
}

func (r *entryReader) next() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, fmt.Errorf("pack entry header at %d truncated", r.off)
	}
	c := r.b[r.pos]
	r.pos++

	return c, nil
}

// typeAndSize reads the first header byte's type and the inflated size,
// four bits there and seven in each continuation byte.
func (r *entryReader) typeAndSize() (packEntry, error) {
	c, err := r.next()
	if err != nil {
		return packEntry{}, err
	}
	e := packEntry{off: r.off, typ: ObjectType(c >> 4 & 7), size: uint64(c & 0x0f)}
	for shift := 4; c&0x80 != 0; shift += 7 {
		if shift > 60 {
			return packEntry{}, fmt.Errorf("pack entry size at %d too long", r.off)
		}
		if c, err = r.next(); err != nil {
			return packEntry{}, err
		}
		e.size |= uint64(c&0x7f) << shift
	}

	return e, nil
}

// ofsDeltaBase reads an OFS_DELTA's base offset, which must lie before the
// entry. git's offset encoding: each continuation adds one before shifting.
func (r *entryReader) ofsDeltaBase() (int64, error) {
	c, err := r.next()
	if err != nil {
		return 0, err
	}
	rel := uint64(c & 0x7f)
	for i := 0; c&0x80 != 0; i++ {
		if i > 8 {
			return 0, fmt.Errorf("pack delta offset at %d too long", r.off)
		}
		if c, err = r.next(); err != nil {
			return 0, err
		}
		rel = (rel+1)<<7 | uint64(c&0x7f)
	}
	off := r.off
	if off < 0 {
		return 0, fmt.Errorf("pack delta at %d has an invalid base offset", off)
	}
	if rel == 0 || rel >= uint64(off) {
		return 0, fmt.Errorf("pack delta at %d has an invalid base offset", off)
	}

	return off - int64(rel), nil
}

// refDeltaBase reads a REF_DELTA's base object id.
func (r *entryReader) refDeltaBase(f Format) (ObjectID, error) {
	if r.pos+f.Size > len(r.b) {
		return ObjectID{}, fmt.Errorf("pack entry header at %d truncated", r.off)
	}
	id, _ := f.IDFromBytes(r.b[r.pos : r.pos+f.Size])
	r.pos += f.Size

	return id, nil
}

// inflate decompresses exactly e.size bytes of e's zlib stream.
func (p *pack) inflate(e packEntry) ([]byte, error) {
	r := io.NewSectionReader(p.data, e.dataOff, p.dataSize-e.dataOff)

	return inflateExact(r, e.size)
}
