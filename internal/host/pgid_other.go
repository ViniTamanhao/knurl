//go:build !unix

package host

import (
	"os/exec"
	"time"
)

// IsolateProcessGroup configures pipe wait delay for non-Unix platforms
// without registering a CommandContext Cancel hook.
func IsolateProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 250 * time.Millisecond
}

// ConfigureProcessGroup configures process termination and wait delay for
// non-Unix platforms.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	IsolateProcessGroup(cmd)
	cmd.Cancel = func() error {
		return SignalProcessGroupKill(cmd)
	}
}

// SignalProcessGroupTerm terminates cmd's process on non-Unix platforms.
func SignalProcessGroupTerm(cmd *exec.Cmd) error {
	return SignalProcessGroupKill(cmd)
}

// SignalProcessGroupKill forcibly kills cmd's process on non-Unix platforms.
func SignalProcessGroupKill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
