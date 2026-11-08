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
// SIGINT to send to another process.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}
