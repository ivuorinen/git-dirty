package git

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The fuzz targets check the decoders' contract on arbitrary input: no
// panic, no result larger than the input allows, and termination. Seeds come
// from files git wrote; `go test` runs the seeds (and testdata/fuzz) as
// ordinary tests.

func gitIndexSeed(f *testing.F) []byte {
	f.Helper()
	dir := newRepo(f, SHA1)
	writeFile(f, dir, "a.txt", "a\n")
	writeFile(f, dir, "d/b.txt", "b\n")
	run(f, dir, "add", "-A")
	run(f, dir, "commit", "-q", "-m", "seed")
	data, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		f.Fatal(err)
	}
	return data
}

func FuzzIndex(f *testing.F) {
	f.Add(gitIndexSeed(f))
	f.Add([]byte("DIRC\x00\x00\x00\x04\x00\x00\x00\x01"))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, format := range formats {
			idx, err := DecodeIndex(data, format)
			if err == nil && len(idx.Entries) > len(data) {
				t.Fatalf("%d entries from %d bytes", len(idx.Entries), len(data))
			}
		}
	})
}

func FuzzEWAH(f *testing.F) {
	f.Add(ewah(70, rlw(true, 1, 1), 0b101))
	f.Add(ewah(200, rlw(false, 2, 1), 1<<3))
	f.Fuzz(func(t *testing.T, data []byte) {
		bits, n, err := decodeEWAH(data)
		if err != nil {
			return
		}
		if n > len(data) {
			t.Fatalf("consumed %d of %d bytes", n, len(data))
		}
		if !sort.IntsAreSorted(bits) || (len(bits) > 0 && bits[len(bits)-1] >= maxEWAHBits) {
			t.Fatalf("bits out of order or range: %v", bits)
		}
	})
}

func FuzzConfig(f *testing.F) {
	f.Add([]byte(configFixture))
	f.Add([]byte("[a \"b\\\"c\"]\n\tk = \"v ; x\" \\\n y\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		ParseConfig(data)
	})
}

func FuzzCommit(f *testing.F) {
	f.Add([]byte("tree 0000000000000000000000000000000000000000\ncommitter C <c@x> 1700000000 +0200\n\nmsg"))
	f.Add([]byte("tree 0000000000000000000000000000000000000000\nparent 1111111111111111111111111111111111111111\ngpgsig x\n y\ncommitter C <c@x> 1 -0130\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		ParseCommit(data, SHA1)
		ParseTag(data, SHA1)
	})
}

func FuzzTree(f *testing.F) {
	f.Add([]byte("100644 a\x00" + string(make([]byte, 20)) + "40000 d\x00" + string(make([]byte, 20))))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, format := range formats {
			entries, err := ParseTree(data, format)
			if err == nil && len(entries)*format.Size > len(data) {
				t.Fatalf("%d entries from %d bytes", len(entries), len(data))
			}
		}
	})
}

// fuzzRepo is a minimal git dir for targets that read files.
func fuzzRepo(t *testing.T) *Repository {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "objects", "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Repository{GitDir: dir, CommonDir: dir, Format: SHA1, Config: &Config{}}
}

func FuzzRefs(f *testing.F) {
	f.Add([]byte("# pack-refs with: peeled fully-peeled sorted \n1111111111111111111111111111111111111111 refs/tags/v1\n^2222222222222222222222222222222222222222\n"), "refs/tags/v1")
	f.Add([]byte("ref: refs/heads/main\n"), "HEAD")
	f.Fuzz(func(t *testing.T, data []byte, name string) {
		r := fuzzRepo(t)
		if err := os.WriteFile(filepath.Join(r.CommonDir, "packed-refs"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.GitDir, "HEAD"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		r.Tags()
		r.Resolve("HEAD")
		if ValidRefName(name) {
			r.Resolve(name)
		}
	})
}

// fuzzPackID is the id every fuzzed one-object pack index maps to offset 12.
var fuzzPackID = SHA1.HashObject(ObjBlob, []byte("fuzz"))

func FuzzPackEntryAndDelta(f *testing.F) {
	content := []byte("hello")
	f.Add(append([]byte{byte(ObjBlob)<<4 | 5}, zlibBytes(content)...), []byte("hello world"), []byte{11, 5, 0x91, 0, 5})
	f.Add(append([]byte{byte(objOfsDelta)<<4 | 3, 0x01}, zlibBytes([]byte{1, 1, 0x90, 1})...), []byte{}, []byte{0x80})
	f.Fuzz(func(t *testing.T, entry, base, delta []byte) {
		applyDelta(base, delta)

		r := fuzzRepo(t)
		packData := append([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"), entry...)
		packData = append(packData, make([]byte, 20)...)
		idx := []byte("\377tOc\x00\x00\x00\x02")
		key := fuzzPackID.Bytes()
		for i := range 256 {
			n := uint32(0)
			if i >= int(key[0]) {
				n = 1
			}
			idx = binary.BigEndian.AppendUint32(idx, n)
		}
		idx = append(idx, key...)
		idx = binary.BigEndian.AppendUint32(idx, 0)
		idx = binary.BigEndian.AppendUint32(idx, 12)
		idx = append(idx, make([]byte, 40)...)
		base2 := filepath.Join(r.CommonDir, "objects", "pack", "pack-f")
		os.WriteFile(base2+".pack", packData, 0o644)
		os.WriteFile(base2+".idx", idx, 0o644)
		r.ReadObject(fuzzPackID)
		r.Close()
	})
}

// idxSeed builds a one-object version 2 pack index mapping id to offset 12,
// directly or through the large-offset table. Seeds are built in memory: a
// git-generated seed would rerun git in every fuzz worker process.
func idxSeed(id ObjectID, large bool) []byte {
	idx := []byte("\377tOc\x00\x00\x00\x02")
	key := id.Bytes()
	for i := range 256 {
		n := uint32(0)
		if i >= int(key[0]) {
			n = 1
		}
		idx = binary.BigEndian.AppendUint32(idx, n)
	}
	idx = append(idx, key...)
	idx = binary.BigEndian.AppendUint32(idx, 0)
	if large {
		idx = binary.BigEndian.AppendUint32(idx, 0x80000000)
		idx = binary.BigEndian.AppendUint64(idx, 12)
	} else {
		idx = binary.BigEndian.AppendUint32(idx, 12)
	}
	return append(idx, make([]byte, 40)...)
}

func FuzzPackIdx(f *testing.F) {
	f.Add(idxSeed(fuzzPackID, false))
	f.Add(idxSeed(fuzzPackID, true))
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "pack-x.idx")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		p, err := openPack(path, SHA1)
		if err != nil {
			return
		}
		defer p.close()
		p.find(fuzzPackID)
		for i := uint32(0); i < p.count && i < 64; i++ {
			p.offset(i)
		}
	})
}
