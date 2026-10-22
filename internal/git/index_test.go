package git

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// indexFixture commits regular, nested, executable and symlink files plus a
// gitlink, so every entry mode appears.
func indexFixture(t *testing.T, f Format) string {
	t.Helper()
	dir := newRepo(t, f)
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "dir/b.txt", "b\n")
	writeFile(t, dir, "dir/sub/c.txt", "c\n")
	writeFile(t, dir, "exec.sh", "#!/bin/sh\n")
	if runtime.GOOS != "windows" {
		//nolint:gosec // G302: the fixture needs the executable bit, so git records mode 100755
		if err := os.Chmod(filepath.Join(dir, "exec.sh"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
			t.Fatal(err)
		}
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "files")
	head := run(t, dir, "rev-parse", headRef)
	run(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
	run(t, dir, "commit", "-q", "-m", "gitlink")
	// An uninitialized submodule is an empty directory.
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}

	return dir
}

// checkEntries compares every entry with git ls-files --stage and the stat
// data with git ls-files --debug.
func checkEntries(t *testing.T, dir string, idx *Index) {
	t.Helper()
	got := make([]string, 0, len(idx.Entries))
	for _, e := range idx.Entries {
		got = append(got, fmt.Sprintf("%06o %s %d\t%s", e.Mode, e.ID, e.Stage, e.Name))
	}
	if want := run(t, dir, "ls-files", "--stage"); strings.Join(got, "\n") != want {
		t.Errorf("entries:\n%s\ngit ls-files --stage:\n%s", strings.Join(got, "\n"), want)
	}

	stat := make([]string, 0, 6*len(idx.Entries))
	for _, e := range idx.Entries {
		stat = append(stat,
			e.Name,
			fmt.Sprintf("  ctime: %d:%d", e.CTime.Unix(), e.CTime.Nanosecond()),
			fmt.Sprintf("  mtime: %d:%d", e.MTime.Unix(), e.MTime.Nanosecond()),
			fmt.Sprintf("  dev: %d\tino: %d", e.Dev, e.Ino),
			fmt.Sprintf("  uid: %d\tgid: %d", e.UID, e.GID),
			fmt.Sprintf("  size: %d", e.Size))
	}
	lines := strings.Split(run(t, dir, "ls-files", "--debug"), "\n")
	want := make([]string, 0, len(lines))
	for _, l := range lines {
		l, _, _ = strings.Cut(l, "\tflags:")
		want = append(want, l)
	}
	if strings.Join(stat, "\n") != strings.Join(want, "\n") {
		t.Errorf("stat data:\n%s\ngit ls-files --debug:\n%s", strings.Join(stat, "\n"), strings.Join(want, "\n"))
	}
}

func readIndex(t *testing.T, dir string) *Index {
	t.Helper()
	idx, err := openRepo(t, dir).ReadIndex()
	if err != nil {
		t.Fatal(err)
	}

	return idx
}

func TestReadIndex_Versions(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name+"/v2 with cache-tree", func(t *testing.T) { testIndexV2(t, f) })
		t.Run(f.Name+"/v3 extended flags", func(t *testing.T) { testIndexV3(t, f) })
		t.Run(f.Name+"/v4 path compression", func(t *testing.T) { testIndexV4(t, f) })
		t.Run(f.Name+"/merge conflict stages", func(t *testing.T) { testIndexConflict(t, f) })
	}
}

func testIndexV2(t *testing.T, f Format) {
	t.Helper()
	dir := indexFixture(t, f)
	idx := readIndex(t, dir)
	if idx.Version != 2 {
		t.Fatalf("version = %d, want 2", idx.Version)
	}
	checkEntries(t, dir, idx)
	if run(t, dir, "status", "--porcelain") != "" {
		t.Fatal("fixture not clean")
	}
	tree := run(t, dir, "write-tree")
	if idx.Tree == nil || idx.Tree.Entries != len(idx.Entries) || idx.Tree.ID.String() != tree {
		t.Errorf("cache-tree = %+v, git write-tree = %s with %d entries", idx.Tree, tree, len(idx.Entries))
	}
}

func testIndexV3(t *testing.T, f Format) {
	t.Helper()
	dir := indexFixture(t, f)
	writeFile(t, dir, "new.txt", "n\n")
	run(t, dir, "add", "-N", "new.txt")
	run(t, dir, "update-index", "--skip-worktree", "a.txt")
	run(t, dir, "update-index", "--assume-unchanged", "dir/b.txt")
	idx := readIndex(t, dir)
	if idx.Version != 3 {
		t.Fatalf("version = %d, want 3", idx.Version)
	}
	checkEntries(t, dir, idx)
	tags := map[string]string{}
	for _, l := range strings.Split(run(t, dir, "ls-files", "-t", "-v"), "\n") {
		tag, name, _ := strings.Cut(l, " ")
		tags[name] = tag
	}
	for _, e := range idx.Entries {
		if wantSkip := tags[e.Name] == "S" || tags[e.Name] == "s"; e.SkipWorktree != wantSkip {
			t.Errorf("%s: SkipWorktree = %v, git ls-files -t says %q", e.Name, e.SkipWorktree, tags[e.Name])
		}
		if wantValid := tags[e.Name] == "h"; e.AssumeValid != wantValid {
			t.Errorf("%s: AssumeValid = %v, git ls-files -v says %q", e.Name, e.AssumeValid, tags[e.Name])
		}
		if wantITA := e.Name == "new.txt"; e.IntentToAdd != wantITA {
			t.Errorf("%s: IntentToAdd = %v", e.Name, e.IntentToAdd)
		}
	}
	st := run(t, dir, "status", "--porcelain=v2")
	if !strings.Contains(st, ".A ") || !strings.Contains(st, "new.txt") {
		t.Error("fixture: git does not see new.txt as intent-to-add")
	}
}

func testIndexV4(t *testing.T, f Format) {
	t.Helper()
	dir := indexFixture(t, f)
	run(t, dir, "update-index", "--index-version", "4")
	idx := readIndex(t, dir)
	if idx.Version != 4 {
		t.Fatalf("version = %d, want 4", idx.Version)
	}
	checkEntries(t, dir, idx)
}

func testIndexConflict(t *testing.T, f Format) {
	t.Helper()
	dir := indexFixture(t, f)
	run(t, dir, "switch", "-q", "-c", "other")
	commit(t, dir, "a.txt", "other\n", "other")
	run(t, dir, "switch", "-q", "main")
	commit(t, dir, "a.txt", "main\n", "main")
	runCode(t, dir, nil, "merge", "-q", "other")
	idx := readIndex(t, dir)
	checkEntries(t, dir, idx)
	stages := 0
	for _, e := range idx.Entries {
		if e.Stage > 0 {
			stages++
		}
	}
	if stages != 3 {
		t.Errorf("conflict stages = %d, want 3", stages)
	}
}

func TestReadIndex_Refusals(t *testing.T) {
	t.Run("sparse index", func(t *testing.T) {
		dir := newRepo(t, SHA1)
		writeFile(t, dir, "A/x.txt", "x\n")
		writeFile(t, dir, "B/y.txt", "y\n")
		run(t, dir, "add", "-A")
		run(t, dir, "commit", "-q", "-m", "dirs")
		run(t, dir, "sparse-checkout", "init", "--cone", "--sparse-index")
		run(t, dir, "sparse-checkout", "set", "A")
		_, err := openRepo(t, dir).ReadIndex()
		if err == nil || !strings.Contains(err.Error(), `"sdir"`) {
			t.Errorf("err = %v, want unsupported extension \"sdir\"", err)
		}
	})
	t.Run("missing index is empty", func(t *testing.T) {
		idx := readIndex(t, newRepo(t, SHA1))
		if len(idx.Entries) != 0 {
			t.Errorf("entries = %d", len(idx.Entries))
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		good, err := os.ReadFile(filepath.Join(indexFixture(t, SHA1), ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		cases := map[string][]byte{
			"bad magic":   append([]byte("XXXX"), good[4:]...),
			"truncated":   good[:len(good)/2],
			"huge count":  append(append(append([]byte{}, good[:8]...), 0xff, 0xff, 0xff, 0xff), good[12:]...),
			"version 9":   append(append(append([]byte{}, good[:4]...), 0, 0, 0, 9), good[8:]...),
			"header only": good[:12],
		}
		for name, data := range cases {
			if _, err := DecodeIndex(data, SHA1); err == nil {
				t.Errorf("%s: want error", name)
			}
		}
	})
}
