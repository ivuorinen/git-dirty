//go:build !windows

package action

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// detach starts git in a session of its own, without a controlling
// terminal, so ssh cannot open /dev/tty to ask for a passphrase or a host
// key. A cancel sends SIGINT to the session's process group, as ctrl+c in
// a terminal does, which reaches git and the helpers it started (ssh,
// git-remote-https); git removes its lock files on SIGINT.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone // git exited before the cancel
		}

		return err
	}
}

// reap kills what is left of git's process group after a cancel. A hook or
// helper that ignores SIGINT would otherwise outlive the TUI, still working
// in the repository. It runs after git has exited and removed its lock
// files; a program that left the group, as a gc that daemonizes does, is
// not touched.
func reap(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // ESRCH: nothing was left
	}
}
