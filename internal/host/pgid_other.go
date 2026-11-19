//go:build !unix

package host

import (
	"os/exec"
	"time"
)

// ConfigureProcessGroup configures process termination and wait delay for
// non-Unix platforms.
func ConfigureProcessGroup(cmd *exec.Cmd) {
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
