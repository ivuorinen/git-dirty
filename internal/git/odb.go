package git

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Limits on object data read from a repository, which may be hostile.
const (
	// maxObjectSize bounds any object or delta result; git-dirty reads
	// commits, trees and tags, which never come near it.
	maxObjectSize = 256 << 20
	// maxDeltaDepth bounds a delta chain. git's pack.depth maximum is 4095;
	// the limit also stops REF_DELTA cycles.
	maxDeltaDepth = 10000
	// baseCacheBytes bounds the per-repository cache of inflated objects.
	baseCacheBytes = 16 << 20
)

// ErrObjectNotFound is returned for an object in no pack and not loose.
var ErrObjectNotFound = errors.New("object not found")

// odb reads objects from loose files and packs. The returned data is shared
// with the cache and must not be modified.
type odb struct {
	dir         string
	f           Format
	packs       []*pack
	packsLoaded bool
	cache       baseCache
}

func (r *Repository) objects() *odb {
	if r.odb == nil {
		r.odb = &odb{dir: filepath.Join(r.CommonDir, "objects"), f: r.Format}
		r.odb.cache.limit = baseCacheBytes
	}

	return r.odb
}

// ReadObject returns an object's type and content. The content must not be
// modified.
func (r *Repository) ReadObject(id ObjectID) (ObjectType, []byte, error) {
	return r.objects().read(id, 0)
}

func (o *odb) close() error {
	var first error
	for _, p := range o.packs {
		if err := p.close(); err != nil && first == nil {
			first = err
		}
	}
	o.packs, o.packsLoaded = nil, false

	return first
}

func (o *odb) loadPacks() error {
	if o.packsLoaded {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(o.dir, "pack"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var idxFiles []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".idx" && e.Type().IsRegular() {
			idxFiles = append(idxFiles, e.Name())
		}
	}
	sort.Strings(idxFiles)
	for _, name := range idxFiles {
		p, err := openPack(filepath.Join(o.dir, "pack", name), o.f)
		if err != nil {
			_ = o.close() // read-only files; the open error is the one to report

			return err
		}
		o.packs = append(o.packs, p)
	}
	o.packsLoaded = true

	return nil
}

func (o *odb) read(id ObjectID, depth int) (ObjectType, []byte, error) {
	if depth > maxDeltaDepth {
		return 0, nil, fmt.Errorf("object %s: delta chain deeper than %d", id, maxDeltaDepth)
	}
	if err := o.loadPacks(); err != nil {
		return 0, nil, err
	}
	for _, p := range o.packs {
		off, ok, err := p.find(id)
		if err != nil {
			return 0, nil, err
		}
		if ok {
			t, data, err := o.readPacked(p, off, depth)
			if err != nil {
				return 0, nil, fmt.Errorf("object %s: %w", id, err)
			}

			return t, data, nil
		}
	}
	t, data, err := o.readLoose(id)
	if errors.Is(err, fs.ErrNotExist) {
		if _, statErr := os.Stat(filepath.Join(o.dir, "info", "alternates")); statErr == nil {
			return 0, nil, fmt.Errorf("object %s: %w (objects/info/alternates is not supported)", id, ErrObjectNotFound)
		}

		return 0, nil, fmt.Errorf("object %s: %w", id, ErrObjectNotFound)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("object %s: %w", id, err)
	}

	return t, data, nil
}

// readPacked resolves the entry at off, following OFS_DELTA bases within the
// pack and REF_DELTA bases through the whole database, then applies the
// deltas from the base outward.
func (o *odb) readPacked(p *pack, off int64, depth int) (ObjectType, []byte, error) {
	chain, baseType, base, err := o.packedBase(p, off, depth)
	if err != nil {
		return 0, nil, err
	}
	for i := len(chain) - 1; i >= 0; i-- {
		e := chain[i]
		delta, err := p.inflate(e)
		if err != nil {
			return 0, nil, fmt.Errorf("pack delta at %d: %w", e.off, err)
		}
		if base, err = applyDelta(base, delta); err != nil {
			return 0, nil, fmt.Errorf("pack delta at %d: %w", e.off, err)
		}
		o.cache.put(p, e.off, baseType, base)
	}

	return baseType, base, nil
}

// packedBase walks the delta chain from the entry at off down to its base:
// a cached object, a REF_DELTA base read through the whole database, or a
// whole object in the pack. It returns the delta entries crossed, outermost
// first, and the base.
func (o *odb) packedBase(p *pack, off int64, depth int) ([]packEntry, ObjectType, []byte, error) {
	var chain []packEntry
	for cur := off; ; {
		if depth+len(chain) > maxDeltaDepth {
			return nil, 0, nil, fmt.Errorf("delta chain deeper than %d", maxDeltaDepth)
		}
		if t, data, ok := o.cache.get(p, cur); ok {
			return chain, t, data, nil
		}
		e, err := p.entry(cur)
		if err != nil {
			return nil, 0, nil, err
		}
		switch e.typ {
		case objOfsDelta:
			chain = append(chain, e)
			cur = e.baseOff

			continue
		case objRefDelta:
			chain = append(chain, e)
			t, data, err := o.read(e.baseID, depth+len(chain))
			if err != nil {
				return nil, 0, nil, err
			}

			return chain, t, data, nil
		case ObjCommit, ObjTree, ObjBlob, ObjTag:
			data, err := p.inflate(e)
			if err != nil {
				return nil, 0, nil, fmt.Errorf("pack offset %d: %w", cur, err)
			}
			o.cache.put(p, cur, e.typ, data)

			return chain, e.typ, data, nil
		}

		// pack.entry accepts no other type.
		return nil, 0, nil, fmt.Errorf("pack offset %d: invalid type %d", cur, e.typ)
	}
}

func (o *odb) readLoose(id ObjectID) (ObjectType, []byte, error) {
	hex := id.String()
	if len(hex) < 3 {
		return 0, nil, fs.ErrNotExist
	}
	f, err := os.Open(filepath.Join(o.dir, hex[:2], hex[2:]))
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = f.Close() }() // read-only
	zr, err := zlib.NewReader(bufio.NewReader(f))
	if err != nil {
		return 0, nil, fmt.Errorf("loose object: %w", err)
	}
	// readExact has checked the stream's end and checksum; Close adds nothing.
	defer func() { _ = zr.Close() }()
	t, size, err := readLooseHeader(zr)
	if err != nil {
		return 0, nil, err
	}
	data, err := readExact(zr, size)
	if err != nil {
		return 0, nil, fmt.Errorf("loose object: %w", err)
	}

	return t, data, nil
}

// readLooseHeader reads a loose object's "<type> <size>\0" header, at most a
// few dozen bytes.
func readLooseHeader(r io.Reader) (ObjectType, uint64, error) {
	var hdr [64]byte
	n := 0
	for {
		if n == len(hdr) {
			return 0, 0, errors.New("loose object header too long")
		}
		if _, err := io.ReadFull(r, hdr[n:n+1]); err != nil {
			return 0, 0, fmt.Errorf("loose object header: %w", err)
		}
		if hdr[n] == 0 {
			break
		}
		n++
	}
	typeName, sizeStr, ok := bytes.Cut(hdr[:n], []byte(" "))
	t, typeOK := parseObjectType(string(typeName))
	size, sizeErr := strconv.ParseUint(string(sizeStr), 10, 64)
	if !ok || !typeOK || sizeErr != nil {
		return 0, 0, fmt.Errorf("loose object: malformed header %q", hdr[:n])
	}
	if size > maxObjectSize {
		return 0, 0, fmt.Errorf("loose object declares %d bytes, over the limit", size)
	}

	return t, size, nil
}

// parseObjectType maps an object header's type name to its type.
func parseObjectType(s string) (ObjectType, bool) {
	for _, t := range [...]ObjectType{ObjCommit, ObjTree, ObjBlob, ObjTag} {
		if t.String() == s {
			return t, true
		}
	}

	return 0, false
}

// inflateExact decompresses a zlib stream that must hold exactly size bytes.
func inflateExact(r io.Reader, size uint64) ([]byte, error) {
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, err
	}
	// readExact has checked the stream's end and checksum; Close adds nothing.
	defer func() { _ = zr.Close() }()

	return readExact(zr, size)
}

// readExact reads exactly size bytes and requires the stream to end there,
// which also lets zlib verify its checksum.
func readExact(r io.Reader, size uint64) ([]byte, error) {
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("content shorter than its declared %d bytes: %w", size, err)
	}
	var extra [1]byte
	n, err := r.Read(extra[:])
	if n > 0 {
		return nil, fmt.Errorf("content longer than its declared %d bytes", size)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if err == nil {
		// A reader may return 0, nil before EOF; one more read settles it.
		if n, err = r.Read(extra[:]); n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			return nil, fmt.Errorf("content longer than its declared %d bytes", size)
		}
	}

	return data, nil
}

// baseCache keeps recently inflated pack objects, keyed by pack and offset,
// within a byte budget; the oldest entries are evicted first.
type baseCache struct {
	limit int
	used  int
	m     map[cacheKey]cacheEntry
	order []cacheKey
}

type cacheKey struct {
	p   *pack
	off int64
}

type cacheEntry struct {
	t    ObjectType
	data []byte
}

func (c *baseCache) get(p *pack, off int64) (ObjectType, []byte, bool) {
	e, ok := c.m[cacheKey{p, off}]

	return e.t, e.data, ok
}

func (c *baseCache) put(p *pack, off int64, t ObjectType, data []byte) {
	if len(data) > c.limit/4 {
		return // too large to be worth holding
	}
	k := cacheKey{p, off}
	if c.m == nil {
		c.m = map[cacheKey]cacheEntry{}
	}
	if _, ok := c.m[k]; ok {
		return
	}
	for c.used+len(data) > c.limit && len(c.order) > 0 {
		old := c.order[0]
		c.order = c.order[1:]
		c.used -= len(c.m[old].data)
		delete(c.m, old)
	}
	c.m[k] = cacheEntry{t, data}
	c.order = append(c.order, k)
	c.used += len(data)
}
