//go:build unix

package host

import (
	"os/exec"
	"syscall"
	"time"
)

// IsolateProcessGroup puts cmd into its own process group with a bounded pipe
// wait delay without registering a CommandContext Cancel hook.
func IsolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 250 * time.Millisecond
}

// ConfigureProcessGroup isolates cmd into its own process group and configures
// SIGKILL group termination on context cancellation so grandchildren cannot leak.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	IsolateProcessGroup(cmd)
	cmd.Cancel = func() error {
		return SignalProcessGroupKill(cmd)
	}
}

// SignalProcessGroupTerm sends SIGTERM to cmd's process group so child workers
// can tear down nested exec subprocess groups gracefully.
func SignalProcessGroupTerm(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// SignalProcessGroupKill sends SIGKILL to cmd's process group.
func SignalProcessGroupKill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
