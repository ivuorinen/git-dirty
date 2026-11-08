//go:build windows

package action

import (
	"os/exec"
	"syscall"
)

// createNoWindow is the CREATE_NO_WINDOW process creation flag.
const createNoWindow = 0x08000000

// detach starts git without a console, so neither git nor ssh can prompt
// in the TUI's. A cancel kills git (exec.Cmd's default): Windows has no
// SIGINT to send to another process. The processes git started, such as
// the git fetch per remote and its remote helper, are not killed; they
// exit when their network operation ends, and WaitDelay bounds the wait
// for the output pipes they hold.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
