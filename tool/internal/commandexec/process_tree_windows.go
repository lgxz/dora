//go:build windows

package commandexec

import (
	"os"
	"os/exec"
	"strconv"
)

// configureProcessTree uses the system taskkill utility to terminate the root
// process and all descendants. CommandContext's default cancellation only
// terminates the root process.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		kill := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		if err := kill.Run(); err == nil {
			return nil
		}
		// If taskkill is unavailable or loses a completion race, retain the
		// direct-process fallback supplied by CommandContext.
		return cmd.Process.Kill()
	}
}
