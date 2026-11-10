//go:build unix

package action

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

// A cancel that arrives after git has exited is not an error.
func TestDetach_CancelAfterExit(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "git", "--version")
	detach(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("Cancel() = %v, want os.ErrProcessDone", err)
	}
}

func TestReap_NothingStarted(_ *testing.T) {
	reap(&exec.Cmd{}) // must not panic on a command that never started
}
