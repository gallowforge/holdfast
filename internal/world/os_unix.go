//go:build unix

package world

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the command in its own process group and makes cancelling
// it kill the whole group.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
