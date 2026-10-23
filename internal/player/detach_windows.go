package player

import (
	"os/exec"
	"syscall"
)

// detachProcess turns a command into a background process: it gets its own
// process group, no console window, and it survives the process that started
// it. The player daemon has to outlive the command that started it, which is
// the whole point of it.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}
}

// createNoWindow is CREATE_NO_WINDOW, and detachProcess is
// DETACHED_PROCESS: neither is exported by the syscall package.
const (
	createNoWindow  = 0x08000000
	detachedProcess = 0x00000008
)
