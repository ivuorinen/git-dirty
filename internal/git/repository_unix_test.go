//go:build unix

package git

import (
	"errors"
	"os"
	"path/filepath"
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
