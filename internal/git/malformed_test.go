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

// Everything read here may come from a hostile clone, and git never writes
// these bytes, so the git CLI cannot be the oracle. Each case is one way of
// breaking a format and names the error it must produce, so a case cannot
// pass by failing for another reason.

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

func TestReadPatterns_Directory(t *testing.T) {
	if _, err := ReadPatterns(t.TempDir(), nil); err == nil {
		t.Error("a directory: want the read error")
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
