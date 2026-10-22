package git

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rawLink decodes the index file as stored, without merging, to inspect the
// link extension git wrote.
func rawLink(t *testing.T, dir string, f Format) *indexLink {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(filepath.Join(dir, ".git", "index")))
	if err != nil {
		t.Fatal(err)
	}
	idx, err := DecodeIndex(data, f)
	if err != nil {
		t.Fatal(err)
	}

	return idx.link
}

func TestReadIndex_SplitIndex(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			dir := indexFixture(t, f)
			// Keep the shared index however much changes, so the bitmaps are
			// exercised instead of git rewriting a fresh shared index.
			run(t, dir, "config", "splitIndex.maxPercentChange", "100")
			run(t, dir, "update-index", "--split-index")
			if rawLink(t, dir, f) == nil {
				t.Fatal("fixture: no link extension after --split-index")
			}
			checkEntries(t, dir, readIndex(t, dir))

			writeFile(t, dir, "a.txt", "changed\n")
			writeFile(t, dir, "zz-new.txt", "new\n")
			run(t, dir, "add", "a.txt", "zz-new.txt")
			run(t, dir, "rm", "-q", "dir/b.txt")
			l := rawLink(t, dir, f)
			if l == nil || len(l.deleted) == 0 || len(l.replaced) == 0 {
				t.Fatalf("fixture: want deletions and replacements in the bitmaps, got %+v", l)
			}
			checkEntries(t, dir, readIndex(t, dir))

			run(t, dir, "update-index", "--no-split-index")
			if rawLink(t, dir, f) != nil {
				t.Fatal("fixture: link extension still present")
			}
			checkEntries(t, dir, readIndex(t, dir))
		})
	}
}

// ewah serializes words with git's EWAH layout, for unit tests.
func ewah(tb testing.TB, bitSize uint32, words ...uint64) []byte {
	tb.Helper()
	n := len(words)
	if n < 0 || n > math.MaxUint32 {
		tb.Fatalf("%d words do not fit the word count", n)

		return nil
	}
	b := binary.BigEndian.AppendUint32(nil, bitSize)
	b = binary.BigEndian.AppendUint32(b, uint32(n))
	for _, w := range words {
		b = binary.BigEndian.AppendUint64(b, w)
	}

	return binary.BigEndian.AppendUint32(b, 0)
}

// rlw builds a run-length word.
func rlw(runBit bool, runLen, literals uint64) uint64 {
	w := runLen<<1 | literals<<33
	if runBit {
		w |= 1
	}

	return w
}

func TestDecodeEWAH(t *testing.T) {
	t.Run("run of ones then a literal", func(t *testing.T) {
		data := ewah(t, 70, rlw(true, 1, 1), 0b101)
		bits, n, err := decodeEWAH(data)
		if err != nil || n != len(data) {
			t.Fatalf("err = %v, consumed %d of %d", err, n, len(data))
		}
		want := make([]int, 0, 66)
		for i := range 64 {
			want = append(want, i)
		}
		want = append(want, 64, 66)
		if !slices.Equal(bits, want) {
			t.Errorf("bits = %v", bits)
		}
	})
	t.Run("zero run is skipped", func(t *testing.T) {
		bits, _, err := decodeEWAH(ewah(t, 200, rlw(false, 2, 1), 1<<3))
		if err != nil || !slices.Equal(bits, []int{131}) {
			t.Errorf("bits = %v, err = %v", bits, err)
		}
	})
	bad := map[string][]byte{
		"truncated header":   {0, 0, 0, 1},
		"words beyond data":  append(ewah(t, 64, rlw(false, 0, 1))[:8], 0, 0, 0, 0),
		"literal count lies": ewah(t, 640, rlw(false, 0, 5), 1),
		"bit beyond size":    ewah(t, 10, rlw(false, 0, 1), 1<<20),
		"run beyond size":    ewah(t, 10, rlw(true, 1, 0)),
		"size too large":     ewah(t, maxEWAHBits+1, rlw(false, 0, 0)),
	}
	for name, data := range bad {
		if _, _, err := decodeEWAH(data); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestReadIndex_SplitIndexErrors(t *testing.T) {
	dir := indexFixture(t, SHA1)
	run(t, dir, "update-index", "--split-index")
	r := openRepo(t, dir)
	l := rawLink(t, dir, SHA1)
	shared := filepath.Join(dir, ".git", "sharedindex."+l.base.String())
	if err := os.Remove(shared); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadIndex(); err == nil || !strings.Contains(err.Error(), "split index") {
		t.Errorf("missing shared index: err = %v", err)
	}
}
