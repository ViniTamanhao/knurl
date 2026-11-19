//go:build !unix

package runtime

import "os"

func applyWorkerRLimits() {}

func workerTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
