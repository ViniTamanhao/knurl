//go:build unix

package runtime

import "syscall"

const maxWorkerNoFile uint64 = 256

// applyWorkerRLimits disables core dumps (to prevent secrets in worker memory
// from persisting to disk) and caps open file descriptors inside _worker.
// Virtual address space (RLIMIT_AS) is left unbounded so Go runtime arenas and
// race-detector shadow memory work reliably; Go heap growth is bounded via
// debug.SetMemoryLimit and the active HeapAlloc watchdog.
func applyWorkerRLimits() {
	var coreLim syscall.Rlimit
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &coreLim)

	var nofile syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &nofile); err == nil {
		if nofile.Cur > maxWorkerNoFile {
			nofile.Cur = maxWorkerNoFile
			_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &nofile)
		}
	}
}
