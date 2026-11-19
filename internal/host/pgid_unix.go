//go:build unix

package host

import (
	"os/exec"
	"syscall"
	"time"
)

// ConfigureProcessGroup isolates cmd into its own process group and configures
// SIGKILL group termination on context cancellation so grandchildren cannot leak.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
