//go:build !unix

package runtime_test

import "os"

func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}
