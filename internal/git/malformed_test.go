package git

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// Everything read here may come from a hostile clone, and git never writes
// these bytes, so the git CLI cannot be the oracle. Each case is one way of
// breaking a format and names the error it must produce, so a case cannot
// pass by failing for another reason.

const (
	blobBody = "world" // a blob's content, five bytes
	wantV2   = "want version 2"
	tooLong  = "too long"

	goosWindows = "windows"
)

func wantErr(t *testing.T, name string, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: err = %v, want one containing %q", name, err, want)
	}
}

// fixedEntry is an index entry's stat data and id, all zero, and its flags.
func fixedEntry(f Format, flags uint16) []byte {
	return binary.BigEndian.AppendUint16(make([]byte, statFieldsSize+f.Size), flags)
}

// rawIndex assembles an index file from its parts, with a zero checksum.
func rawIndex(f Format, version, count uint32, parts ...[]byte) []byte {
	b := binary.BigEndian.AppendUint32([]byte("DIRC"), version)
	b = binary.BigEndian.AppendUint32(b, count)

	return append(append(b, slices.Concat(parts...)...), make([]byte, f.Size)...)
}

func rawExt(sig string, body []byte) []byte {
	return append(
		binary.BigEndian.AppendUint32([]byte(sig), uint32(len(body)&math.MaxUint32)),
		body...)
}

func named(f Format, flags uint16, name string) []byte {
	return append(fixedEntry(f, flags), name...)
}

func TestDecodeIndex_Malformed(t *testing.T) {
	id := strings.Repeat("\x01", SHA1.Size)
	tree := func(body string) []byte { return rawIndex(SHA1, 2, 0, rawExt("TREE", []byte(body))) }
	link := func(body string) []byte { return rawIndex(SHA1, 2, 0, rawExt("link", []byte(body))) }
	none := string(ewah(t, 8))
	const truncated = "index truncated"
	const badCounts = "malformed counts"
	for _, c := range []struct {
		name string
		f    Format
		data []byte
		want string
	}{
		{"version 1", SHA1, rawIndex(SHA1, 1, 0), "unsupported index version 1"},
		{"extended flags in version 2", SHA1, rawIndex(SHA1, 2, 1, fixedEntry(SHA1, flagExtended)), "extended flags"},
		{"extended flags cut short", SHA1, rawIndex(SHA1, 3, 1, fixedEntry(SHA1, flagExtended)), truncated},
		{"entry cut short", SHA256, rawIndex(SHA256, 2, 1, make([]byte, minIndexEntrySize)), truncated},
		{"v4 strips too much", SHA1, rawIndex(SHA1, 4, 1, named(SHA1, 0, "\x05a\x00")), "strips 5 of 0"},
		{"v4 name without a NUL", SHA1, rawIndex(SHA1, 4, 1, named(SHA1, 0, "\x00a")), "not terminated"},
		{"v4 varint cut short", SHA1, rawIndex(SHA1, 4, 1, named(SHA1, 0, "\x80")), "varint"},
		{"v4 varint too long", SHA1, rawIndex(SHA1, 4, 1, named(SHA1, 0, strings.Repeat("\x80", 11))), "varint"},
		{"long name without a NUL", SHA1, rawIndex(SHA1, 2, 1, named(SHA1, flagNameMask, "abc")), "not terminated"},
		{"name past the end", SHA1, rawIndex(SHA1, 2, 1, named(SHA1, 100, "a")), truncated},
		{"name length mismatch", SHA1, rawIndex(SHA1, 2, 1, named(SHA1, 1, "ab\x00")), "length mismatch"},
		{"padding past the end", SHA1, rawIndex(SHA1, 2, 1, named(SHA1, 2, "ab\x00")), truncated},
		{"extension header cut short", SHA1, rawIndex(SHA1, 2, 0, []byte{1, 2, 3}), truncated},
		{"extension body past the end", SHA1, rawIndex(SHA1, 2, 0, []byte("TREE\x00\x00\x00\x64")), `extension "TREE"`},
		{"mandatory extension", SHA1, rawIndex(SHA1, 2, 0, rawExt("abcd", nil)), `extension "abcd"`},
		{"signature below A", SHA1, rawIndex(SHA1, 2, 0, rawExt("\x01bcd", nil)), "unsupported index extension"},
		{"signature above Z", SHA1, rawIndex(SHA1, 2, 0, rawExt("[bcd", nil)), "unsupported index extension"},
		{"TREE without a root", SHA1, tree("x\x00"), "missing root"},
		{"TREE without a newline", SHA1, tree("\x000 0"), "TREE extension: truncated"},
		{"TREE without a space", SHA1, tree("\x000\n"), badCounts},
		{"TREE entries not a number", SHA1, tree("\x00x 0\n"), badCounts},
		{"TREE subtrees not a number", SHA1, tree("\x000 x\n"), badCounts},
		{"TREE negative subtrees", SHA1, tree("\x000 -1\n"), badCounts},
		{"TREE entries below -1", SHA1, tree("\x00-2 0\n"), badCounts},
		{"TREE id cut short", SHA1, tree("\x001 0\nabc"), "truncated id"},
		{"link cut short", SHA1, link("abc"), "link extension: truncated"},
		{"link delete bitmap bad", SHA1, link(id + "abc"), "delete bitmap"},
		{"link replace bitmap bad", SHA1, link(id + none + "abc"), "replace bitmap"},
		{"link trailing bytes", SHA1, link(id + none + none + "x"), "trailing bytes"},
	} {
		_, err := DecodeIndex(c.data, c.f)
		wantErr(t, c.name, err, c.want)
	}
}

func TestDecodeIndex_Accepted(t *testing.T) {
	if _, err := DecodeIndex(rawIndex(SHA1, 2, 0, rawExt("REUC", []byte("x"))), SHA1); err != nil {
		t.Errorf("optional extension: %v, want it skipped", err)
	}
	idx, err := DecodeIndex(rawIndex(SHA1, 2, 0, rawExt("TREE", []byte("\x00-1 0\n"))), SHA1)
	if err != nil || idx.Tree == nil || idx.Tree.Entries != -1 {
		t.Errorf("invalidated cache-tree: %+v, %v", idx, err)
	}
}

// A path too long for the entry's 12-bit length is read up to its NUL.
func TestReadIndex_LongName(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			dir := newRepo(t, f)
			blob := hashObject(t, dir, "blob", "x\n")
			long := strings.Repeat("d/", flagNameMask/2+1) + "f"
			run(t, dir, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+long)
			checkEntries(t, dir, readIndex(t, dir))
		})
	}
}

func TestReadIndex_Unreadable(t *testing.T) {
	dir := newRepo(t, SHA1)
	index := filepath.Join(dir, ".git", "index")
	writeFile(t, dir, ".git/index", "junk")
	_, err := openRepo(t, dir).ReadIndex()
	wantErr(t, "corrupt", err, "not an index file")

	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(index, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := openRepo(t, dir).ReadIndex(); err == nil {
		t.Error("index is a directory: want error, not an empty index")
	}
}

// splitFixture writes a split index naming a shared index with the given
// content, and returns its reader. link builds the index from the link
// extension's base id.
func splitFixture(t *testing.T, shared []byte, index func(base string) []byte) *Repository {
	t.Helper()
	dir := newRepo(t, SHA1)
	base := strings.Repeat("ab", SHA1.Size)
	writeFile(t, dir, ".git/sharedindex."+base, string(shared))
	id, err := SHA1.ParseHex(base)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".git/index", string(index(string(id.b[:id.n]))))

	return openRepo(t, dir)
}

func TestReadIndex_SplitIndexMalformed(t *testing.T) {
	bit0 := string(ewah(t, 8, rlw(false, 0, 1), 1))
	none := string(ewah(t, 8))
	withLink := func(bitmaps string, count uint32, parts ...[]byte) func(string) []byte {
		return func(base string) []byte {
			return rawIndex(SHA1, 2, count, append(parts, rawExt("link", []byte(base+bitmaps)))...)
		}
	}
	entry := named(SHA1, 1, "a\x00")
	shared0 := rawIndex(SHA1, 2, 0)
	split := func(base string) []byte { return rawIndex(SHA1, 2, 0, rawExt("link", []byte(base))) }
	for _, c := range []struct {
		name   string
		shared []byte
		index  func(string) []byte
		want   string
	}{
		{"corrupt shared index", []byte("junk"), split, "not an index file"},
		{"shared index is itself split", split(strings.Repeat("a", SHA1.Size)), split, "itself split"},
		{"deleted position past the shared entries", shared0, withLink(bit0+none, 0), "beyond 0 shared entries"},
		{"replaced position past the shared entries", shared0, withLink(none+bit0, 0), "beyond 0 shared entries"},
		{"more replacements than entries", rawIndex(SHA1, 2, 1, entry), withLink(none+bit0, 0), "1 replacements but 0"},
		{"a named replacement", rawIndex(SHA1, 2, 1, entry), withLink(none+bit0, 1, named(SHA1, 1, "b\x00")), "has a name"},
	} {
		_, err := splitFixture(t, c.shared, c.index).ReadIndex()
		wantErr(t, c.name, err, c.want)
	}

	// git drops an entry both replaced and deleted.
	nameless := named(SHA1, 0, "\x00\x00")
	dropped, err := splitFixture(t, rawIndex(SHA1, 2, 1, entry), withLink(bit0+bit0, 1, nameless)).ReadIndex()
	if err != nil || len(dropped.Entries) != 0 {
		t.Errorf("replaced and deleted: %+v, %v, want no entries", dropped, err)
	}

	// The split part usually has no TREE of its own.
	treeBody := "\x000 0\n" + strings.Repeat("\x02", SHA1.Size)
	idx, err := splitFixture(t, rawIndex(SHA1, 2, 0, rawExt("TREE", []byte(treeBody))), split).ReadIndex()
	if err != nil || idx.Tree == nil || idx.Tree.Entries != 0 {
		t.Errorf("shared cache-tree: %+v, %v", idx, err)
	}
}

func TestDecodeEWAH_RunPastTheEnd(t *testing.T) {
	_, _, err := decodeEWAH(ewah(t, 1, rlw(false, 1000, 0)))
	wantErr(t, "zero run", err, "run extends beyond")
}

func packHeader(entry ...byte) []byte {
	return append([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"), entry...)
}

// writePackFiles writes pack and idx as a pack in dir's object store.
func writePackFiles(t *testing.T, dir string, pack, idx []byte) {
	t.Helper()
	writeFile(t, dir, ".git/objects/pack/pack-test.pack", string(pack))
	writeFile(t, dir, ".git/objects/pack/pack-test.idx", string(idx))
}

// openRawPack opens pack and idx directly, for checks of one entry.
func openRawPack(t *testing.T, pack, idx []byte) *pack {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "pack-x.pack", string(pack))
	writeFile(t, dir, "pack-x.idx", string(idx))
	p, err := openPack(filepath.Join(dir, "pack-x.idx"), SHA1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.close(); err != nil {
			t.Error(err)
		}
	})

	return p
}

// patched returns a copy of b with v written at offset at.
func patched(b []byte, at int, v ...byte) []byte {
	out := slices.Clone(b)
	copy(out[at:], v)

	return out
}

func TestOpenPack_Malformed(t *testing.T) {
	good := idxSeed(fuzzPackID, false)
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"shorter than its header", good[:100], "pack index header"},
		{"bad magic", patched(good, 0, 'X'), wantV2},
		{"version 1", patched(good, 7, 1), wantV2},
		{"fanout goes down", patched(good, idxHeaderSize, 0xff, 0xff, 0xff, 0xff), "not monotonic"},
		{"too small for its objects", good[:idxHeaderSize+idxFanoutSize], "too small"},
	} {
		dir := t.TempDir()
		writeFile(t, dir, "pack-x.idx", string(c.data))
		_, err := openPack(filepath.Join(dir, "pack-x.idx"), SHA1)
		wantErr(t, c.name, err, c.want)
	}
	if _, err := openPack(filepath.Join(t.TempDir(), "missing.idx"), SHA1); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing index: err = %v", err)
	}
}

func TestPackFind(t *testing.T) {
	good := idxSeed(fuzzPackID, true)
	const offsetAt = idxHeaderSize + idxFanoutSize + 20 + 4 // the name and the CRC
	const tableAt = offsetAt + 4
	const outOfRange = "large offset out of range"
	for _, c := range []struct {
		name string
		data []byte
	}{
		{"past the table", patched(good, offsetAt, 0x80, 0, 0, 5)},
		{"over 1<<62", patched(good, tableAt, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)},
	} {
		_, ok, err := openRawPack(t, packHeader(), c.data).find(fuzzPackID)
		wantErr(t, c.name, err, outOfRange)
		if ok {
			t.Errorf("%s: found = true alongside the error", c.name)
		}
	}
	p := openRawPack(t, packHeader(), good)
	if _, ok, err := p.find(SHA256.HashObject(ObjBlob, nil)); ok || err != nil {
		t.Errorf("id of another format: found = %v, err = %v, want not found", ok, err)
	}
	if _, ok, err := p.find(SHA1.IDFromBytes(make([]byte, SHA1.Size))); ok || err != nil {
		t.Errorf("id in the first fanout bucket: found = %v, err = %v, want not found", ok, err)
	}
}

// packEntryHeader is an entry's type and inflated size, in git's encoding.
func packEntryHeader(typ byte, size uint64) []byte {
	c := typ<<4 | byte(size&0x0f)
	size >>= 4
	var out []byte
	for size > 0 {
		out = append(out, c|0x80)
		c = byte(size & 0x7f)
		size >>= 7
	}

	return append(out, c)
}

func TestPackEntry_Malformed(t *testing.T) {
	const blob, ofs, ref = byte(ObjBlob) << 4, byte(objOfsDelta) << 4, byte(objRefDelta) << 4
	const cut = "truncated"
	const badBase = "invalid base offset"
	idx := idxSeed(fuzzPackID, false)
	repeat := func(first, b byte, n int) []byte {
		return append([]byte{first}, bytes.Repeat([]byte{b}, n)...)
	}
	for _, c := range []struct {
		name  string
		entry []byte
		want  string
	}{
		{"size cut short", []byte{blob | 0x80}, cut},
		{"size too long", repeat(blob|0x80, 0x80, 9), tooLong},
		{"over the object limit", packEntryHeader(byte(ObjBlob), maxObjectSize+1), "over the limit"},
		{"reserved type 0", []byte{0}, "invalid type 0"},
		{"reserved type 5", []byte{5 << 4}, "invalid type 5"},
		{"delta offset missing", []byte{ofs}, cut},
		{"delta offset cut short", []byte{ofs, 0x80}, cut},
		{"delta offset too long", repeat(ofs, 0xff, 12), tooLong},
		{"delta offset zero", []byte{ofs, 0}, badBase},
		{"delta base before the pack", []byte{ofs, 0x7f}, badBase},
		{"delta base id cut short", []byte{ref, 1, 2, 3}, cut},
	} {
		_, err := openRawPack(t, packHeader(c.entry...), idx).entry(12)
		wantErr(t, c.name, err, c.want)
	}
	p := openRawPack(t, packHeader(blob), idx)
	const outside = "out of range"
	_, err := p.entry(11)
	wantErr(t, "inside the pack header", err, outside)
	_, err = p.entry(p.dataSize)
	wantErr(t, "at the end of the pack", err, outside)
}

func TestReadObject_MalformedPack(t *testing.T) {
	base := SHA1.HashObject(ObjBlob, []byte(blobBody))
	id := SHA1.HashObject(ObjBlob, []byte("packed"))
	blob := append(packEntryHeader(byte(ObjBlob), 5), zlibBytes(t, []byte(blobBody))...)
	refDelta := func(delta []byte) []byte {
		e := append(packEntryHeader(byte(objRefDelta), uint64(len(delta))), base.b[:base.n]...)

		return append(e, zlibBytes(t, delta)...)
	}
	badStream := append(packEntryHeader(byte(objRefDelta), 5), base.b[:base.n]...)
	badStream = append(badStream, "junk!"...)
	missing := SHA1.HashObject(ObjBlob, []byte("absent"))
	refMissing := append(packEntryHeader(byte(objRefDelta), 1), missing.b[:missing.n]...)
	refMissing = append(refMissing, zlibBytes(t, []byte{0})...)
	badBlob := append(packEntryHeader(byte(ObjBlob), 5), "junk!"...)
	idx := idxSeed(id, false)
	for _, c := range []struct {
		name string
		pack []byte
		idx  []byte
		want string
	}{
		{"pack shorter than its header", []byte("PACK"), idx, "header"},
		{"bad pack magic", []byte("XXXX\x00\x00\x00\x02\x00\x00\x00\x01x"), idx, "not a version 2 or 3"},
		{"pack version 4", []byte("PACK\x00\x00\x00\x04\x00\x00\x00\x01x"), idx, "not a version 2 or 3"},
		{"object stream not zlib", packHeader(badBlob...), idx, "pack offset 12"},
		{"delta stream not zlib", packHeader(badStream...), idx, "pack delta at 12"},
		{"delta does not fit its base", packHeader(refDelta([]byte{9, 1, 'x'})...), idx, "delta base size"},
		{"corrupt large offset", packHeader(blob...), patched(idxSeed(id, true), 1056, 0x80, 0, 0, 5), "large offset"},
		{"delta base absent", packHeader(refMissing...), idx, "object not found"},
		{"corrupt pack index", packHeader(blob...), patched(idx, 0, 'X'), wantV2},
	} {
		dir := newRepo(t, SHA1)
		hashObject(t, dir, "blob", blobBody)
		writePackFiles(t, dir, c.pack, c.idx)
		_, _, err := openRepo(t, dir).ReadObject(id)
		wantErr(t, c.name, err, c.want)
	}
}

func TestReadObject_PackFiles(t *testing.T) {
	blob := append(packEntryHeader(byte(ObjBlob), 5), zlibBytes(t, []byte(blobBody))...)
	id := SHA1.HashObject(ObjBlob, []byte(blobBody))

	t.Run("version 3", func(t *testing.T) {
		dir := newRepo(t, SHA1)
		v3 := patched(packHeader(blob...), 7, 3)
		writePackFiles(t, dir, v3, idxSeed(id, false))
		if _, data, err := openRepo(t, dir).ReadObject(id); err != nil || string(data) != blobBody {
			t.Errorf("data = %q, err = %v", data, err)
		}
	})
	t.Run("index without its pack", func(t *testing.T) {
		dir := newRepo(t, SHA1)
		writeFile(t, dir, ".git/objects/pack/pack-test.idx", string(idxSeed(id, false)))
		if _, _, err := openRepo(t, dir).ReadObject(id); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want not-exist", err)
		}
	})
	t.Run("a directory named like an index is skipped", func(t *testing.T) {
		dir := newRepo(t, SHA1)
		if err := os.Mkdir(filepath.Join(dir, ".git", "objects", "pack", "x.idx"), 0o750); err != nil {
			t.Fatal(err)
		}
		hex := hashObject(t, dir, "blob", blobBody)
		if _, data, err := openRepo(t, dir).ReadObject(mustParse(t, openRepo(t, dir), hex)); err != nil ||
			string(data) != blobBody {
			t.Errorf("data = %q, err = %v", data, err)
		}
	})
}

// The depth limit is counted across the chain the walk has already crossed.
func TestReadObject_PackDirectoryIsAFile(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("Go's ReadDir on Windows reads a file as an empty directory")
	}
	dir := newRepo(t, SHA1)
	pack := filepath.Join(dir, ".git", "objects", "pack")
	if err := os.RemoveAll(pack); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".git/objects/pack", "not a directory")
	id := SHA1.HashObject(ObjBlob, []byte(blobBody))
	if _, _, err := openRepo(t, dir).ReadObject(id); err == nil || errors.Is(err, ErrObjectNotFound) {
		t.Errorf("err = %v, want the read error", err)
	}
}

func TestPackedBase_DepthLimit(t *testing.T) {
	blob := append(packEntryHeader(byte(ObjBlob), 5), zlibBytes(t, []byte(blobBody))...)
	deltaOff := int64(12 + len(blob))
	delta := append(packEntryHeader(byte(objOfsDelta), 1), byte((deltaOff-12)&0x7f))
	delta = append(delta, zlibBytes(t, []byte{0})...)
	p := openRawPack(t, packHeader(append(blob, delta...)...), idxSeed(fuzzPackID, false))
	_, _, _, err := (&odb{f: SHA1}).packedBase(p, deltaOff, maxDeltaDepth)
	wantErr(t, "chain over the limit", err, "delta chain deeper than")
}

func TestApplyDelta_Malformed(t *testing.T) {
	const cut = "truncated"
	for _, c := range []struct {
		name  string
		base  string
		delta []byte
		want  string
	}{
		{"result size cut short", "", []byte{0, 0x80}, cut},
		{"size over 63 bits", "", bytes.Repeat([]byte{0x80}, 11), tooLong},
		{"copy size cut short", blobBody, []byte{5, 5, 0x90}, cut},
		{"copy past the declared size", blobBody, []byte{5, 2, 0x90, 5}, "past its declared size"},
	} {
		_, err := applyDelta([]byte(c.base), c.delta)
		wantErr(t, c.name, err, c.want)
	}
}

func TestReadLoose_Malformed(t *testing.T) {
	const badHeader = "malformed header"
	hex := "ab" + strings.Repeat("cd", SHA1.Size-1)
	for _, c := range []struct {
		name string
		file []byte
		want string
	}{
		{"not zlib", []byte("junk"), "loose object:"},
		{"header never ends", zlibBytes(t, bytes.Repeat([]byte{'a'}, 80)), "header too long"},
		{"header cut short", zlibBytes(t, []byte("blob 1")), "loose object header"},
		{"no size", zlibBytes(t, []byte("blob\x00")), badHeader},
		{"unknown type", zlibBytes(t, []byte("bad 1\x00x")), badHeader},
		{"size not a number", zlibBytes(t, []byte("blob x\x00")), badHeader},
		{"over the object limit", zlibBytes(t, []byte("blob 99999999999\x00")), "over the limit"},
		{"shorter than declared", zlibBytes(t, []byte("blob 5\x00hi")), "shorter"},
		{"longer than declared", zlibBytes(t, []byte("blob 1\x00hi")), "longer"},
	} {
		dir := newRepo(t, SHA1)
		writeFile(t, dir, ".git/objects/"+hex[:2]+"/"+hex[2:], string(c.file))
		r := openRepo(t, dir)
		_, _, err := r.ReadObject(mustParse(t, r, hex))
		wantErr(t, c.name, err, c.want)
	}
}

func TestReadObject_NotFound(t *testing.T) {
	dir := newRepo(t, SHA1)
	r := openRepo(t, dir)
	if _, _, err := r.ReadObject(ObjectID{}); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("zero id: err = %v", err)
	}
	writeFile(t, dir, ".git/objects/info/alternates", "/elsewhere\n")
	_, _, err := r.ReadObject(SHA1.HashObject(ObjBlob, []byte("absent")))
	if !errors.Is(err, ErrObjectNotFound) || !strings.Contains(err.Error(), "alternates") {
		t.Errorf("with alternates: err = %v, want not found, naming alternates", err)
	}
}

func TestBaseCache(t *testing.T) {
	var c baseCache
	p := &pack{}
	if c.put(p, 1, ObjBlob, make([]byte, baseCacheBytes/4+1)); len(c.m) != 0 {
		t.Error("an object over a quarter of the budget was cached")
	}
	quarter := make([]byte, baseCacheBytes/4)
	for off := range int64(5) {
		c.put(p, off, ObjBlob, quarter)
	}
	if _, _, ok := c.get(p, 0); ok {
		t.Error("the oldest entry survived going over the budget")
	}
	if _, _, ok := c.get(p, 4); !ok {
		t.Error("the newest entry is gone")
	}
	if c.used != baseCacheBytes {
		t.Errorf("used = %d, want %d", c.used, baseCacheBytes)
	}
	if c.put(p, 4, ObjBlob, quarter); c.used != baseCacheBytes {
		t.Errorf("a second put of one key counted twice: used = %d", c.used)
	}
}

func TestReadObjects_WrongOrBroken(t *testing.T) {
	dir := newRepo(t, SHA1)
	commit(t, dir, "a.txt", "a\n", "init")
	r := openRepo(t, dir)
	parse := func(typ, content string) ObjectID { return mustParse(t, r, hashObject(t, dir, typ, content)) }
	blob, missing := parse("blob", "x"), SHA1.HashObject(ObjBlob, []byte("absent"))
	badTree := parse("tree", "junk")
	nest := parse("tree", "40000 sub\x00"+string(badTree.b[:badTree.n]))
	good := mustParse(t, r, run(t, dir, "rev-parse", "HEAD^{tree}"))
	var none = func(string, TreeEntry) error { return nil }
	stop := errors.New("stop")

	_, err := r.ReadCommit(blob)
	wantErr(t, "commit of a blob", err, "not a commit")
	_, err = r.ReadCommit(missing)
	wantErr(t, "missing commit", err, "object not found")
	_, err = r.ReadCommit(parse("commit", "junk\n\n"))
	wantErr(t, "malformed commit", err, "missing tree")
	_, err = r.Peel(missing)
	wantErr(t, "peel of a missing object", err, "object not found")
	_, err = r.Peel(parse("tag", "junk\n\n"))
	wantErr(t, "malformed tag", err, "missing object or type")
	wantErr(t, "tree of a blob", r.WalkTree(blob, none), "not a tree")
	wantErr(t, "missing tree", r.WalkTree(missing, none), "object not found")
	wantErr(t, "malformed tree", r.WalkTree(badTree, none), "tree entry")
	wantErr(t, "malformed subtree", r.WalkTree(nest, none), "tree entry")
	wantErr(t, "tree too deep", r.walkTree(good, "", none, maxTreeDepth+1), "deeper than")
	if err := r.WalkTree(good, func(string, TreeEntry) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("callback error: %v", err)
	}
}

func TestParseCommit_Accepted(t *testing.T) {
	id := strings.Repeat("ab", SHA1.Size)
	head := "tree " + id + "\ncommitter "
	for _, c := range []struct {
		sig    string
		offset int
	}{
		{"C <c@x> 7 -0130", -5400},
		{"C <c@x> 7 +0200", 7200},
		{"C <c@x> 7", 0},       // no zone
		{"C <c@x> 7 +02", 0},   // too short
		{"C <c@x> 7 x0200", 0}, // no sign
		{"C <c@x> 7 +ab00", 0}, // hours not a number
		{"C <c@x> 7 +00cd", 0}, // minutes not a number
	} {
		got, err := ParseCommit([]byte(head+c.sig), SHA1) // ends without a blank line
		if err != nil {
			t.Errorf("%q: %v", c.sig, err)

			continue
		}
		if _, off := got.CommitTime.Zone(); off != c.offset || got.CommitTime.Unix() != 7 {
			t.Errorf("%q: time %v, offset %d, want offset %d", c.sig, got.CommitTime, off, c.offset)
		}
	}
	tag, err := ParseTag([]byte("object "+id+"\ntype commit"), SHA1) // ends without a blank line
	if err != nil || tag.Type != ObjCommit {
		t.Errorf("tag: %+v, %v", tag, err)
	}
}

func TestPackedRefs_Malformed(t *testing.T) {
	id := strings.Repeat("ab", SHA1.Size)
	const name = "refs/heads/a"
	const bad = "malformed"
	const badPeeled = "bad peeled line"
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		{"peeled line first", "^" + id + "\n", badPeeled},
		{"peeled line not an id", id + " " + name + "\n^zz\n", badPeeled},
		{"no space", id + "\n", bad},
		{"not an id", "zz " + name + "\n", bad},
		{"invalid name", id + " refs/heads/a..b\n", bad},
	} {
		dir := newRepo(t, SHA1)
		writeFile(t, dir, ".git/packed-refs", c.body)
		r := openRepo(t, dir)
		_, err := r.Resolve("refs/heads/missing")
		wantErr(t, c.name+" (Resolve)", err, c.want)
		_, err = r.Tags()
		wantErr(t, c.name+" (Tags)", err, c.want)
	}

	dir := newRepo(t, SHA1)
	writeFile(t, dir, ".git/packed-refs", "\n"+id+" "+name+"\n")
	if got, err := openRepo(t, dir).Resolve(name); err != nil || got.String() != id {
		t.Errorf("blank line: %v, %v", got, err)
	}

	dir = newRepo(t, SHA1)
	if err := os.Mkdir(filepath.Join(dir, ".git", "packed-refs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := openRepo(t, dir).Resolve(name); err == nil || errors.Is(err, ErrRefNotFound) {
		t.Errorf("packed-refs is a directory: err = %v, want the read error", err)
	}
}

func TestReadRef_NoHEAD(t *testing.T) {
	dir := newRepo(t, SHA1)
	if err := os.Remove(filepath.Join(dir, ".git", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if _, err := openRepo(t, dir).ReadRef(headRef); !errors.Is(err, ErrRefNotFound) {
		t.Errorf("err = %v, want not found", err)
	}
}

func TestLooseRefs_Malformed(t *testing.T) {
	dir := refRepo(t, SHA1)
	writeFile(t, dir, ".git/refs/heads/sym", "ref: refs/heads/a..b\n")
	_, err := openRepo(t, dir).ReadRef("refs/heads/sym")
	wantErr(t, "symbolic target", err, "invalid symbolic target")

	tags, err := openRepo(t, dir).Tags()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".git/refs/tags/v1.lock", "junk\n")
	withLock, err := openRepo(t, dir).Tags()
	if err != nil || len(withLock) != len(tags) {
		t.Errorf("lock file: %d tags (err %v), want the %d it had", len(withLock), err, len(tags))
	}
	writeFile(t, dir, ".git/refs/tags/garbage", "junk\n")
	_, err = openRepo(t, dir).Tags()
	wantErr(t, "garbage tag", err, "invalid content")
}

// locked sets path's mode to 0 until the test ends, and skips the test where
// that stops nothing: for root, and on Windows.
func locked(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // G302: a directory has to be searchable
			t.Error(err)
		}
	})
	f, err := os.Open(filepath.Clean(path))
	if err == nil {
		_ = f.Close() // read-only
		t.Skip("mode 0 does not stop this user")
	}
}

// A ref the user cannot read is an error, not an absent ref.
func TestRefs_PermissionDenied(t *testing.T) {
	dir := refRepo(t, SHA1)
	gitDir := filepath.Join(dir, ".git")
	id := strings.Repeat("ab", SHA1.Size) + "\n"
	for _, c := range []struct {
		name string
		path string
		read func(r *Repository) error
	}{
		{"a loose ref", "refs/heads/locked", func(r *Repository) error {
			_, err := r.ReadRef("refs/heads/locked")

			return err
		}},
		{"packed-refs", "packed-refs", func(r *Repository) error {
			_, err := r.Resolve("refs/heads/absent")

			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			writeFile(t, gitDir, c.path, id)
			locked(t, filepath.Join(gitDir, c.path))
			if err := c.read(openRepo(t, dir)); !errors.Is(err, fs.ErrPermission) {
				t.Errorf("err = %v, want a permission error", err)
			}
		})
	}
}

func TestRefs_PermissionDeniedOnDirectories(t *testing.T) {
	dir := refRepo(t, SHA1)
	locked(t, filepath.Join(dir, ".git", "refs", "tags"))
	if _, err := openRepo(t, dir).Tags(); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("unreadable refs/tags: err = %v", err)
	}
	dir = refRepo(t, SHA1)
	locked(t, filepath.Join(dir, ".git", "refs", "heads"))
	if _, err := openRepo(t, dir).ReadRef(refMain); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("a ref below an unsearchable directory: err = %v", err)
	}
}

// Per-worktree refs live in the worktree's git dir: one worktree's are not
// another's.
func TestRefs_PerWorktree(t *testing.T) {
	dir := worktreeRepo(t)
	wt := filepath.Join(filepath.Dir(dir), "wt")
	addWorktree(t, dir, wt)
	for _, name := range []string{"refs/bisect/bad", "refs/worktree/x", "refs/rewritten/y"} {
		run(t, wt, "update-ref", name, "HEAD")
		got, err := openRepo(t, wt).Resolve(name)
		if want := run(t, wt, "rev-parse", name); err != nil || got.String() != want {
			t.Errorf("%s in the worktree: %v, %v, git says %s", name, got, err, want)
		}
		if _, code := runCode(t, dir, nil, "rev-parse", "--verify", "-q", name); code == 0 {
			t.Fatalf("fixture: the main worktree sees %s", name)
		}
		if _, err := openRepo(t, dir).Resolve(name); !errors.Is(err, ErrRefNotFound) {
			t.Errorf("%s in the main worktree: err = %v, want not found", name, err)
		}
	}
}

func TestMapRef_Skips(t *testing.T) {
	negative := specs(t, "^refs/heads/secret", defaultFetch)
	if got, ok := MapRef(negative, "refs/heads/feature"); !ok || got != "refs/remotes/origin/feature" {
		t.Errorf("a negative refspec that does not match: %q, %v", got, ok)
	}
	if got, ok := MapRef(specs(t, defaultFetch), "elsewhere/refs/heads/x"); ok {
		t.Errorf("name outside the refspec's prefix mapped to %q", got)
	}
	if _, err := ParseRefSpec("^refs/*/*"); err == nil {
		t.Error("negative refspec with two wildcards: want error")
	}
}

func TestObjectType_String(t *testing.T) {
	for typ, want := range map[ObjectType]string{
		ObjCommit:     "commit",
		ObjTree:       "tree",
		ObjBlob:       "blob",
		ObjTag:        "tag",
		objOfsDelta:   "unknown(6)",
		objRefDelta:   "unknown(7)",
		ObjectType(0): "unknown(0)",
	} {
		if got := typ.String(); got != want {
			t.Errorf("ObjectType(%d) = %q, want %q", int(typ), got, want)
		}
	}
}

func TestMatcher_Domain(t *testing.T) {
	const domain = "sub"
	p, ok := ParsePattern("x", []string{domain})
	if !ok {
		t.Fatal("pattern skipped")
	}
	folding := Matcher{Patterns: []Pattern{p}, IgnoreCase: true}
	exact := Matcher{Patterns: []Pattern{p}}
	for _, c := range []struct {
		name string
		m    Matcher
		path []string
		want bool
	}{
		{"below the domain", exact, []string{domain, "x"}, true},
		{"the domain itself", exact, []string{domain}, false},
		{"another directory", exact, []string{"other", "x"}, false},
		{"another case, exact", exact, []string{"SUB", "x"}, false},
		{"another case, folding", folding, []string{"SUB", "x"}, true},
	} {
		if got := c.m.Ignored(c.path, false); got != c.want {
			t.Errorf("%s: Ignored = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMatchSegments(t *testing.T) {
	for _, c := range []struct {
		pat, parts []string
		want       bool
	}{
		{[]string{"a", "b"}, []string{"a", "b", "c"}, false},
		{[]string{"a", "**"}, []string{"a", "b"}, true},
		{[]string{"a", "**"}, []string{"a"}, false},
	} {
		if got := matchSegments(c.pat, c.parts, false); got != c.want {
			t.Errorf("matchSegments(%v, %v) = %v, want %v", c.pat, c.parts, got, c.want)
		}
	}
	if globMatch("[", "x", false) {
		t.Error(`a malformed glob matched, git's "[" never does`)
	}
}

func TestReadPatterns_Directory(t *testing.T) {
	if _, err := ReadPatterns(t.TempDir(), nil); err == nil {
		t.Error("a directory: want the read error")
	}
}

func TestOpen_Refusals(t *testing.T) {
	t.Run("malformed config", func(t *testing.T) {
		dir := newRepo(t, SHA1)
		writeFile(t, dir, ".git/config", "[bad\n")
		_, err := OpenWorktree(dir)
		wantErr(t, "config", err, "bad config line")
	})
	t.Run("unknown repository format", func(t *testing.T) {
		dir := newRepo(t, SHA1)
		writeFile(t, dir, ".git/config", "[core]\n\trepositoryformatversion = 99\n")
		if _, err := OpenWorktree(dir); err == nil {
			t.Error("want error")
		}
	})
	t.Run("malformed config.worktree", func(t *testing.T) {
		dir := newRepo(t, SHA1)
		writeFile(t, dir, ".git/config",
			"[core]\n\trepositoryformatversion = 1\n[extensions]\n\tworktreeconfig = true\n")
		writeFile(t, dir, ".git/config.worktree", "[bad\n")
		_, err := OpenWorktree(dir)
		wantErr(t, "config.worktree", err, "bad config line")
	})
}

// A commondir file may hold an absolute path; git writes a relative one.
func TestOpen_AbsoluteCommonDir(t *testing.T) {
	dir := worktreeRepo(t)
	wt := filepath.Join(filepath.Dir(dir), "wt")
	addWorktree(t, dir, wt)
	common := filepath.Join(dir, ".git")
	writeFile(t, common, "worktrees/wt/commondir", common+"\n")
	r := openRepo(t, wt)
	got, err1 := os.Stat(r.CommonDir)
	want, err2 := os.Stat(run(t, wt, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if err := errors.Join(err1, err2); err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(got, want) {
		t.Errorf("CommonDir = %s, git names another directory", r.CommonDir)
	}
}

func TestListWorktrees_Skips(t *testing.T) {
	if _, _, err := ListWorktrees(t.TempDir()); err == nil {
		t.Error("not a repository: want error")
	}
	dir := worktreeRepo(t)
	addWorktree(t, dir, filepath.Join(filepath.Dir(dir), "wt"))
	writeFile(t, dir, ".git/worktrees/stray", "a file, not a worktree")
	wts, _, err := ListWorktrees(dir)
	if err != nil || len(wts) != 1 || wts[0].ID != "wt" {
		t.Errorf("worktrees = %+v, err = %v, want only wt", wts, err)
	}

	locked(t, filepath.Join(dir, ".git", "worktrees"))
	if _, _, err := ListWorktrees(dir); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("unreadable worktrees directory: err = %v", err)
	}
}

// A git dir that cannot be searched says so, instead of listing no worktrees.
func TestListWorktrees_UnsearchableGitDir(t *testing.T) {
	dir := worktreeRepo(t)
	locked(t, filepath.Join(dir, ".git"))
	if _, _, err := ListWorktrees(dir); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("err = %v, want a permission error", err)
	}
}

func TestFindGitDir_NamesARegularFile(t *testing.T) {
	wt := t.TempDir()
	writeFile(t, wt, "elsewhere", "a file")
	writeFile(t, wt, ".git", "gitdir: elsewhere\n")
	if _, code := runCode(t, wt, nil, "rev-parse", "--git-dir"); code == 0 {
		t.Fatal("fixture: git accepts it")
	}
	if _, err := OpenWorktree(wt); !errors.Is(err, errNotGitRepo) {
		t.Errorf("err = %v, want %v", err, errNotGitRepo)
	}
}

func TestIsGitDir_Refusals(t *testing.T) {
	t.Run("objects is a file", func(t *testing.T) {
		dir := bareRepo(t, SHA1)
		objects := filepath.Join(dir, "objects")
		if err := os.RemoveAll(objects); err != nil {
			t.Fatal(err)
		}
		writeFile(t, dir, "objects", "a file")
		if IsGitDir(dir) {
			t.Error("true, want false")
		}
	})
	t.Run("HEAD cannot be read", func(t *testing.T) {
		dir := bareRepo(t, SHA1)
		locked(t, filepath.Join(dir, "HEAD"))
		if IsGitDir(dir) {
			t.Error("true, want false")
		}
	})
}

func TestReadObject_NoPackDirectory(t *testing.T) {
	dir := newRepo(t, SHA1)
	hex := hashObject(t, dir, "blob", blobBody)
	if err := os.RemoveAll(filepath.Join(dir, ".git", "objects", "pack")); err != nil {
		t.Fatal(err)
	}
	r := openRepo(t, dir)
	if _, data, err := r.ReadObject(mustParse(t, r, hex)); err != nil || string(data) != blobBody {
		t.Errorf("data = %q, err = %v, want the loose object", data, err)
	}
}

func TestReadPatterns_NothingToRead(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "file", "*.log\n")
	for name, path := range map[string]string{
		"missing":      filepath.Join(dir, "absent"),
		"below a file": filepath.Join(dir, "file", "below"),
		"null device":  os.DevNull,
	} {
		if ps, err := ReadPatterns(path, nil); err != nil || len(ps) != 0 {
			t.Errorf("%s: %v, %v, want no patterns and no error", name, ps, err)
		}
	}
	locked(t, filepath.Join(dir, "file"))
	if ps, err := ReadPatterns(filepath.Join(dir, "file"), nil); err != nil || len(ps) != 0 {
		t.Errorf("unreadable: %v, %v, want no patterns and no error", ps, err)
	}
}

func TestReadGitignore(t *testing.T) {
	dir := t.TempDir()
	if ps := ReadGitignore(filepath.Join(dir, ".gitignore"), nil); len(ps) != 0 {
		t.Errorf("missing: %v", ps)
	}
	writeFile(t, dir, ".gitignore", "*.log\n")
	if ps := ReadGitignore(filepath.Join(dir, ".gitignore"), nil); len(ps) != 1 {
		t.Errorf("regular file: %v, want one pattern", ps)
	}
	if ps := ReadGitignore(dir, nil); len(ps) != 0 {
		t.Errorf("a directory: %v", ps)
	}
	locked(t, filepath.Join(dir, ".gitignore"))
	if ps := ReadGitignore(filepath.Join(dir, ".gitignore"), nil); len(ps) != 0 {
		t.Errorf("unreadable: %v", ps)
	}
}
