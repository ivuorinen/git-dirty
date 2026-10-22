package git

import (
	"fmt"
	"strings"
	"testing"
)

// hashObject writes raw object content with git and returns its id.
func hashObject(t *testing.T, dir, typ, content string) string {
	t.Helper()
	out, code := runCode(t, dir, []byte(content), "hash-object", "-t", typ, "-w", "--stdin", "--literally")
	if code != 0 {
		t.Fatalf("hash-object: %s", out)
	}
	return out
}

func checkCommit(t *testing.T, dir string, r *Repository, rev string) {
	t.Helper()
	id, err := r.Format.ParseHex(run(t, dir, "rev-parse", rev))
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.ReadCommit(id)
	if err != nil {
		t.Fatal(err)
	}
	var parents []string
	for _, p := range c.Parents {
		parents = append(parents, p.String())
	}
	got := fmt.Sprintf("%s|%s|%d", c.Tree, strings.Join(parents, " "), c.CommitTime.Unix())
	if want := run(t, dir, "log", "-1", "--format=%T|%P|%ct", rev); got != want {
		t.Errorf("%s: %s, git log says %s", rev, got, want)
	}
}

func TestParseCommit_MatchesGit(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			dir := newRepo(t, f)
			commit(t, dir, "a.txt", "a\n", "root")
			for _, b := range []string{"b1", "b2", "b3"} {
				run(t, dir, "switch", "-q", "-c", b, "main")
				commit(t, dir, b+".txt", b+"\n", b)
			}
			run(t, dir, "switch", "-q", "main")
			run(t, dir, "merge", "-q", "--no-edit", "b1", "b2", "b3")
			if n := strings.Count(run(t, dir, "log", "-1", "--format=%P"), " ") + 1; n != 3 {
				t.Fatalf("fixture: merge has %d parents, want an octopus of 3", n)
			}

			tree := run(t, dir, "rev-parse", "HEAD^{tree}")
			parent := run(t, dir, "rev-parse", "HEAD")
			signed := "tree " + tree + "\nparent " + parent +
				"\nauthor A U Thor <a@example.invalid> 1700000000 +0200" +
				"\ncommitter C O Mitter <c@example.invalid> 1700000123 -0130" +
				"\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEE\n -----END PGP SIGNATURE-----" +
				"\nmergetag object " + parent + "\n type commit\n tag v0\n tagger T <t@example.invalid> 1700000000 +0000\n \n msg" +
				"\n\nsigned commit\n\ntree " + strings.Repeat("0", 2*f.Size) + " in the message is not a header\n"
			signedID := hashObject(t, dir, "commit", signed)
			if out := run(t, dir, "cat-file", "-p", signedID); !strings.Contains(out, "gpgsig") {
				t.Fatalf("fixture: %s", out)
			}

			r := openRepo(t, dir)
			for _, rev := range []string{"HEAD", "HEAD^1", "b2", signedID} {
				checkCommit(t, dir, r, rev)
			}
		})
	}
}

func TestWalkTree_MatchesLsTree(t *testing.T) {
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			dir := indexFixture(t, f)
			r := openRepo(t, dir)
			c, err := r.ReadCommit(mustParse(t, r, run(t, dir, "rev-parse", "HEAD")))
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			err = r.WalkTree(c.Tree, func(path string, e TreeEntry) error {
				got = append(got, fmt.Sprintf("%06o %s\t%s", e.Mode, e.ID, path))
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			want := run(t, dir, "ls-tree", "-r", "--format=%(objectmode) %(objectname)%x09%(path)", "HEAD")
			if strings.Join(got, "\n") != want {
				t.Errorf("WalkTree:\n%s\ngit ls-tree -r:\n%s", strings.Join(got, "\n"), want)
			}
		})
	}
}

func mustParse(t *testing.T, r *Repository, hex string) ObjectID {
	t.Helper()
	id, err := r.Format.ParseHex(hex)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestParseTree_CanonicalModes(t *testing.T) {
	dir := newRepo(t, SHA1)
	blob := mustParse(t, openRepo(t, dir), hashObject(t, dir, "blob", "x\n"))
	raw := "100664 legacy\x00" + string(blob.Bytes()) + "100775 legacyexec\x00" + string(blob.Bytes())
	entries, err := ParseTree([]byte(raw), SHA1)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Mode != ModeRegular || entries[1].Mode != ModeExecutable {
		t.Errorf("modes = %o, %o; want 100644, 100755", entries[0].Mode, entries[1].Mode)
	}
}

func TestPeel_TagChain(t *testing.T) {
	for _, f := range formats {
		dir := newRepo(t, f)
		commit(t, dir, "a.txt", "a\n", "one")
		run(t, dir, "tag", "-a", "-m", "first", "t1")
		run(t, dir, "tag", "-a", "-m", "second", "t2", "t1")
		r := openRepo(t, dir)
		got, err := r.Peel(mustParse(t, r, run(t, dir, "rev-parse", "t2")))
		if err != nil || got.String() != run(t, dir, "rev-parse", "t2^{}") {
			t.Errorf("%s: Peel(t2) = %s, %v", f.Name, got, err)
		}
	}
}

func TestParseObjects_Errors(t *testing.T) {
	id := strings.Repeat("ab", 20)
	commits := map[string]string{
		"missing tree":      "parent " + id + "\ncommitter C <c@x> 1 +0000\n\nm",
		"missing committer": "tree " + id + "\n\nm",
		"bad tree id":       "tree xyz\ncommitter C <c@x> 1 +0000\n\nm",
		"bad timestamp":     "tree " + id + "\ncommitter C <c@x> soon +0000\n\nm",
		"duplicate tree":    "tree " + id + "\ntree " + id + "\ncommitter C <c@x> 1 +0000\n\nm",
	}
	for name, c := range commits {
		if _, err := ParseCommit([]byte(c), SHA1); err == nil {
			t.Errorf("commit %s: want error", name)
		}
	}
	trees := map[string]string{
		"no mode":      "nomode",
		"bad mode":     "10x644 a\x00" + strings.Repeat("\x01", 20),
		"no name":      "100644 \x00" + strings.Repeat("\x01", 20),
		"truncated id": "100644 a\x00\x01\x02",
	}
	for name, tr := range trees {
		if _, err := ParseTree([]byte(tr), SHA1); err == nil {
			t.Errorf("tree %s: want error", name)
		}
	}
	if _, err := ParseTag([]byte("type commit\n\nmsg"), SHA1); err == nil {
		t.Error("tag without object: want error")
	}
}
