package git

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
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
	file, err := os.Open(idxPath)
	if err != nil {
		return nil, err
	}
	p := &pack{name: strings.TrimSuffix(idxPath, ".idx"), f: f, idx: file}
	if err := p.readIdxHeader(); err != nil {
		file.Close()
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
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	var hdr [12]byte
	if _, err := file.ReadAt(hdr[:], 0); err != nil {
		file.Close()
		return fmt.Errorf("%s.pack: header: %w", p.name, err)
	}
	if v := binary.BigEndian.Uint32(hdr[4:8]); string(hdr[:4]) != "PACK" || (v != 2 && v != 3) {
		file.Close()
		return fmt.Errorf("%s.pack: not a version 2 or 3 pack", p.name)
	}
	p.data, p.dataSize = file, fi.Size()
	return nil
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
	if err := p.openData(); err != nil {
		return packEntry{}, err
	}
	if off < 12 || off >= p.dataSize {
		return packEntry{}, fmt.Errorf("pack offset %d out of range", off)
	}
	var buf [16 + maxHashSize]byte
	n, err := p.data.ReadAt(buf[:], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return packEntry{}, err
	}
	b := buf[:n]
	pos := 0
	next := func() (byte, error) {
		if pos >= len(b) {
			return 0, fmt.Errorf("pack entry header at %d truncated", off)
		}
		c := b[pos]
		pos++
		return c, nil
	}
	c, err := next()
	if err != nil {
		return packEntry{}, err
	}
	e := packEntry{off: off, typ: ObjectType(c >> 4 & 7), size: uint64(c & 0x0f)}
	for shift := 4; c&0x80 != 0; shift += 7 {
		if shift > 60 {
			return packEntry{}, fmt.Errorf("pack entry size at %d too long", off)
		}
		if c, err = next(); err != nil {
			return packEntry{}, err
		}
		e.size |= uint64(c&0x7f) << shift
	}
	switch e.typ {
	case ObjCommit, ObjTree, ObjBlob, ObjTag:
	case objOfsDelta:
		// git's offset encoding: each continuation adds one before shifting.
		if c, err = next(); err != nil {
			return packEntry{}, err
		}
		rel := uint64(c & 0x7f)
		for i := 0; c&0x80 != 0; i++ {
			if i > 8 {
				return packEntry{}, fmt.Errorf("pack delta offset at %d too long", off)
			}
			if c, err = next(); err != nil {
				return packEntry{}, err
			}
			rel = (rel+1)<<7 | uint64(c&0x7f)
		}
		if rel == 0 || rel >= uint64(off) {
			return packEntry{}, fmt.Errorf("pack delta at %d has an invalid base offset", off)
		}
		e.baseOff = off - int64(rel)
	case objRefDelta:
		if pos+p.f.Size > len(b) {
			return packEntry{}, fmt.Errorf("pack entry header at %d truncated", off)
		}
		e.baseID, _ = p.f.IDFromBytes(b[pos : pos+p.f.Size])
		pos += p.f.Size
	default:
		return packEntry{}, fmt.Errorf("pack entry at %d has invalid type %d", off, e.typ)
	}
	if e.size > maxObjectSize {
		return packEntry{}, fmt.Errorf("pack entry at %d declares %d bytes, over the limit", off, e.size)
	}
	e.dataOff = off + int64(pos)
	return e, nil
}

// inflate decompresses exactly e.size bytes of e's zlib stream.
func (p *pack) inflate(e packEntry) ([]byte, error) {
	r := io.NewSectionReader(p.data, e.dataOff, p.dataSize-e.dataOff)
	return inflateExact(r, e.size)
}
