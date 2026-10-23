//go:build !windows

package player

import (
	"os/exec"
	"syscall"
)

// detachProcess turns a command into a background process: it gets its own
// session, so it survives the shell that started it and never receives the
// terminal's hangup.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
