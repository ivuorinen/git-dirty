package git

import (
	"bytes"
	"compress/zlib"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var realRepos = flag.Bool("realrepos", false, "self-check every object of the five largest repositories under ~/Code")

// historyFixture builds a history whose objects delta well: one large file
// edited a line at a time, plus a few others.
func historyFixture(tb testing.TB, f Format) string {
	tb.Helper()
	dir := newRepo(tb, f)
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d of a file that is edited a little in every commit", i)
	}
	for c := range 30 {
		lines[(c*7)%len(lines)] = fmt.Sprintf("edited in commit %d", c)
		writeFile(tb, dir, "big.txt", strings.Join(lines, "\n")+"\n")
		writeFile(tb, dir, fmt.Sprintf("dir%d/file.txt", c%3), fmt.Sprintf("content %d\n", c))
		run(tb, dir, "add", "-A")
		run(tb, dir, "commit", "-q", "-m", fmt.Sprintf("commit %d", c))
		if c%10 == 0 {
			run(tb, dir, "tag", "-a", "-m", "tag", fmt.Sprintf("v%d", c))
		}
	}

	return dir
}

// selfCheck reads every object git lists and checks its type, size, and that
// hashing the content reproduces its id. It returns the object count.
func selfCheck(tb testing.TB, dir string) int {
	tb.Helper()
	r, err := OpenWorktree(dir)
	if err != nil {
		r, err = Open(dir) // a bare repository
	}
	if err != nil {
		tb.Fatal(err)
	}
	defer closeRepo(tb, r)
	list := run(tb, dir, "cat-file", "--batch-all-objects", "--batch-check=%(objectname) %(objecttype) %(objectsize)")
	n := 0
	for _, line := range strings.Split(list, "\n") {
		parts := strings.Fields(line)
		if len(parts) != 3 {
			tb.Fatalf("unexpected cat-file line %q", line)
		}
		id, err := r.Format.ParseHex(parts[0])
		if err != nil {
			tb.Fatal(err)
		}
		typ, data, err := r.ReadObject(id)
		if err != nil {
			tb.Errorf("ReadObject(%s): %v", parts[0], err)

			continue
		}
		if typ.String() != parts[1] || strconv.Itoa(len(data)) != parts[2] {
			tb.Errorf("%s: %s %d, git says %s %s", parts[0], typ, len(data), parts[1], parts[2])
		}
		if got := r.Format.HashObject(typ, data); got != id {
			tb.Errorf("%s: content hashes to %s", parts[0], got)
		}
		n++
	}

	return n
}

// closeRepo closes r, failing the test on an error.
func closeRepo(tb testing.TB, r *Repository) {
	tb.Helper()
	if err := r.Close(); err != nil {
		tb.Errorf("Close(%s): %v", r.GitDir, err)
	}
}

// entryTypes counts the pack entry types in a repository's packs.
func entryTypes(t *testing.T, dir string) map[ObjectType]int {
	t.Helper()
	r := openRepo(t, dir)
	o := r.objects()
	if err := o.loadPacks(); err != nil {
		t.Fatal(err)
	}
	counts := map[ObjectType]int{}
	for _, p := range o.packs {
		for i := range p.count {
			off, err := p.offset(i)
			if err != nil {
				t.Fatal(err)
			}
			e, err := p.entry(off)
			if err != nil {
				t.Fatal(err)
			}
			counts[e.typ]++
		}
	}

	return counts
}

func TestODB_SelfCheck(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name+"/loose", func(t *testing.T) {
			dir := historyFixture(t, f)
			if c := entryTypes(t, dir); len(c) != 0 {
				t.Fatalf("fixture has packed objects: %v", c)
			}
			if n := selfCheck(t, dir); n < 100 {
				t.Errorf("only %d objects checked", n)
			}
		})
		t.Run(f.Name+"/gc", func(t *testing.T) {
			dir := historyFixture(t, f)
			run(t, dir, "gc", "-q", "--prune=now")
			selfCheck(t, dir)
		})
		t.Run(f.Name+"/deep OFS_DELTA", func(t *testing.T) {
			dir := historyFixture(t, f)
			run(t, dir, "repack", "-adfq", "--depth=50", "--window=250")
			if c := entryTypes(t, dir); c[objOfsDelta] == 0 {
				t.Fatalf("fixture has no OFS_DELTA entries: %v", c)
			}
			selfCheck(t, dir)
		})
		t.Run(f.Name+"/REF_DELTA", func(t *testing.T) {
			dir := historyFixture(t, f)
			run(t, dir, "-c", "repack.useDeltaBaseOffset=false", "repack", "-adfq")
			c := entryTypes(t, dir)
			if c[objRefDelta] == 0 || c[objOfsDelta] != 0 {
				t.Fatalf("fixture entry types: %v, want REF_DELTA only", c)
			}
			selfCheck(t, dir)
		})
	}
}

// writePack writes a one-object pack and its index into dir's object store.
// largeOffset stores the offset through the 8-byte table.
func writePack(t *testing.T, dir string, f Format, id ObjectID, entry []byte, largeOffset bool) {
	t.Helper()
	packData := append([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"), entry...)
	packData = append(packData, make([]byte, f.Size)...) // trailer, not verified
	base := filepath.Join(dir, ".git", "objects", "pack", "pack-test")
	if err := os.WriteFile(base+".pack", packData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".idx", idxSeed(id, largeOffset), 0o600); err != nil {
		t.Fatal(err)
	}
}

func zlibBytes(tb testing.TB, b []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}

	return buf.Bytes()
}

func TestODB_LargeOffset(t *testing.T) {
	const content = "hello"
	dir := newRepo(t, SHA1)
	id := SHA1.HashObject(ObjBlob, []byte(content))
	entry := append([]byte{byte(ObjBlob)<<4 | byte(len(content))}, zlibBytes(t, []byte(content))...)
	writePack(t, dir, SHA1, id, entry, true)
	typ, data, err := openRepo(t, dir).ReadObject(id)
	if err != nil || typ != ObjBlob || string(data) != content {
		t.Errorf("ReadObject = %v %q %v", typ, data, err)
	}
}

func TestODB_SelfReferencingRefDelta(t *testing.T) {
	dir := newRepo(t, SHA1)
	id := SHA1.HashObject(ObjBlob, []byte("loop"))
	delta := zlibBytes(t, []byte{4, 4, 4, 'l', 'o', 'o', 'p'})
	entry := append([]byte{byte(objRefDelta)<<4 | 7}, id.Bytes()...)
	entry = append(entry, delta...)
	writePack(t, dir, SHA1, id, entry, false)
	_, _, err := openRepo(t, dir).ReadObject(id)
	if err == nil || !strings.Contains(err.Error(), "deeper than") {
		t.Errorf("err = %v, want a delta depth error", err)
	}
}

func TestODB_CorruptLoose(t *testing.T) {
	dir := newRepo(t, SHA1)
	r := openRepo(t, dir)
	cases := map[string][]byte{
		"shorter than declared": zlibBytes(t, []byte("blob 10\x00abc")),
		"longer than declared":  zlibBytes(t, []byte("blob 3\x00abcdef")),
		"bad type":              zlibBytes(t, []byte("nope 3\x00abc")),
		"over the size limit":   zlibBytes(t, []byte(fmt.Sprintf("blob %d\x00", maxObjectSize+1))),
		"not zlib":              []byte("plain text"),
	}
	for name, body := range cases {
		id := SHA1.HashObject(ObjBlob, []byte(name))
		hex := id.String()
		p := filepath.Join(dir, ".git", "objects", hex[:2], hex[2:])
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.ReadObject(id); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	_, _, err := r.ReadObject(SHA1.HashObject(ObjBlob, []byte("absent")))
	if err == nil || !strings.Contains(err.Error(), ErrObjectNotFound.Error()) {
		t.Errorf("absent object: err = %v", err)
	}
}

func TestApplyDelta(t *testing.T) {
	base := []byte("hello world")
	ok := []struct {
		name  string
		base  []byte
		delta []byte
		want  string
	}{
		{"copy", base, []byte{11, 5, 0x91, 0, 5}, "hello"},
		{"copy and insert", base, []byte{11, 8, 0x91, 6, 5, 3, '!', '!', '!'}, "world!!!"},
		{
			"copy size zero means 64 KiB",
			bytes.Repeat([]byte{'x'}, 0x10000),
			[]byte{0x80, 0x80, 4, 0x80, 0x80, 4, 0x80},
			strings.Repeat("x", 0x10000),
		},
	}
	for _, tt := range ok {
		got, err := applyDelta(tt.base, tt.delta)
		if err != nil || string(got) != tt.want {
			t.Errorf("%s: got %d bytes, %v", tt.name, len(got), err)
		}
	}
	bad := map[string][]byte{
		"wrong base size":       {10, 5, 0x91, 0, 5},
		"copy out of range":     {11, 5, 0x91, 8, 5},
		"insert past target":    {11, 2, 3, 'a', 'b', 'c'},
		"reserved opcode":       {11, 1, 0},
		"truncated copy":        {11, 5, 0x91},
		"truncated insert":      {11, 3, 3, 'a'},
		"result shorter":        {11, 10, 0x91, 0, 5},
		"result over the limit": {11, 0x80, 0x80, 0x80, 0x80, 0x10},
		"truncated header":      {0x80},
	}
	keys := make([]string, 0, len(bad))
	for k := range bad {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if _, err := applyDelta(base, bad[name]); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// realRepo is a repository TestODB_RealRepos checks, with its pack size.
type realRepo struct {
	dir  string
	size int64
}

// largestRepos finds the repositories with packs below root and returns the
// n with the most pack bytes. Unreadable directories are skipped.
func largestRepos(t *testing.T, root string, n int) []realRepo {
	t.Helper()
	var repos []realRepo
	// The root is the developer's own ~/Code, from HOME or GIT_DIRTY_REAL_HOME.
	//nolint:gosec // G703: an opt-in development check that walks a directory the developer names
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			t.Logf("skipping %s: %v", p, err)

			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "node_modules" || d.Name() == "vendor" {
			return filepath.SkipDir
		}
		packDir := filepath.Join(p, ".git", "objects", "pack")
		if fi, err := os.Stat(packDir); err == nil && fi.IsDir() {
			repos = append(repos, realRepo{p, packBytes(packDir)})

			return filepath.SkipDir
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].size > repos[j].size })
	if len(repos) > n {
		repos = repos[:n]
	}

	return repos
}

// packBytes sums the sizes of the files in a pack directory.
func packBytes(dir string) int64 {
	var size int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			size += info.Size()
		}
	}

	return size
}

// TestODB_RealRepos is a development check, off by default: run with
// go test ./internal/git -run TestODB_RealRepos -realrepos -timeout 60m.
func TestODB_RealRepos(t *testing.T) {
	if !*realRepos {
		t.Skip("enable with -realrepos")
	}
	home, _ := os.UserHomeDir()
	// TestMain points HOME at a temp dir; use the real one from the password
	// file's view via the original environment variable if set.
	if h := os.Getenv("GIT_DIRTY_REAL_HOME"); h != "" {
		home = h
	}
	repos := largestRepos(t, filepath.Join(home, "Code"), 5)
	if len(repos) == 0 {
		t.Fatal("no repositories found; set GIT_DIRTY_REAL_HOME")
	}
	for _, r := range repos {
		n := selfCheck(t, r.dir)
		t.Logf("%s: %d objects, %d MB of packs", r.dir, n, r.size>>20)
	}
}
