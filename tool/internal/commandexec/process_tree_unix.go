//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package commandexec

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcessTree starts the command in a fresh process group and makes
// context cancellation kill that whole group. This includes the shell and
// ordinary descendants such as `timeout python3 ...`.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
}
