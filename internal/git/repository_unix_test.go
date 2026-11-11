//go:build unix

package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestOpenRegular opens what a raced Lstat can let through. A FIFO must be
// refused without waiting for a writer, which needs O_NONBLOCK, and a device
// by the handle's Stat. Unix only: Windows has no syscall.Mkfifo.
func TestOpenRegular(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "file", "x")
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	for _, c := range []struct {
		name string
		path string
		want error
	}{
		{"regular file", filepath.Join(dir, "file"), nil},
		{"FIFO", fifo, errNotRegular},
		{"device", os.DevNull, errNotRegular},
	} {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				f, err := OpenRegular(c.path)
				if err == nil {
					_ = f.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, c.want) {
					t.Fatalf("OpenRegular(%s) = %v, want %v", c.path, err, c.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("OpenRegular(%s) blocked", c.path)
			}
		})
	}
}

// TestOpen_BoundedReads: IsGitDir lets the walk open folders a clone or an
// archive checked out (git never checks out a .git path, but it does check
// out HEAD, objects/, refs/ and a symlinked config), so config and
// commondir must refuse a device and a file over the size cap at once.
func TestOpen_BoundedReads(t *testing.T) {
	for _, c := range []struct {
		name string
		make func(t *testing.T, p string)
		file string
	}{
		{"config links to /dev/zero", linkDevZero, configName},
		{"commondir links to /dev/zero", linkDevZero, "commondir"},
		{"config over the size cap", func(t *testing.T, p string) {
			t.Helper()
			body := "[core]\n\tbare = true\n" + strings.Repeat("# padding\n", maxConfigFile/10+1)
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}, configName},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := bareRepo(t, SHA1)
			p := filepath.Join(dir, c.file)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			c.make(t, p)
			done := make(chan error, 1)
			go func() {
				r, err := Open(dir)
				if err == nil {
					_ = r.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Errorf("Open succeeded with %s", c.name)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("Open did not return with %s", c.name)
			}
		})
	}
}

// configName is a repository's config file.
const configName = "config"

func linkDevZero(t *testing.T, p string) {
	t.Helper()
	if err := os.Symlink("/dev/zero", p); err != nil {
		t.Skip(err)
	}
}

// TestIsGitDir_FIFOHead: a FIFO HEAD is refused without waiting for a
// writer. There is no oracle: git's open() of the FIFO would block.
func TestIsGitDir_FIFOHead(t *testing.T) {
	dir := bareRepo(t, SHA1)
	head := filepath.Join(dir, "HEAD")
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(head, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan bool, 1)
	go func() { done <- IsGitDir(dir) }()
	select {
	case got := <-done:
		if got {
			t.Error("IsGitDir with a FIFO HEAD = true, want false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("IsGitDir blocked on a FIFO HEAD")
	}
}

// A .gitignore is not followed when it is a symlink, as in git since 2.32: a
// clone could point one at a FIFO, whose open blocks, or at a device that
// never ends. A FIFO fails this test by hanging; /dev/zero would use up the
// machine's memory.
func TestReadGitignore_Symlinks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real", "*.log\n")
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	for _, target := range []string{"real", fifo} {
		link := filepath.Join(t.TempDir(), ".gitignore")
		if err := os.Symlink(filepath.Join(dir, filepath.Base(target)), link); err != nil {
			t.Fatal(err)
		}
		if ps := ReadGitignore(link, nil); len(ps) != 0 {
			t.Errorf("a link to %s was followed: %v", target, ps)
		}
	}
}

// core.excludesFile can come from a repository's config: a FIFO must not
// block the scan. A symlink to a regular file is followed, as git does.
func TestReadPatterns_FollowsOnlyToARegularFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "real", "*.log\n")
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "real"), link); err != nil {
		t.Fatal(err)
	}
	if ps, err := ReadPatterns(link, nil); err != nil || len(ps) != 1 {
		t.Errorf("link to a file: %v, %v, want one pattern", ps, err)
	}
	if ps, err := ReadPatterns(fifo, nil); err != nil || len(ps) != 0 {
		t.Errorf("a FIFO: %v, %v, want no patterns and no error", ps, err)
	}
}
