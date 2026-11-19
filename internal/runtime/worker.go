package runtime

import (
	"context"
	"fmt"
	"io"
	"os/signal"
	"runtime/debug"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	// DefaultWorkerMemoryLimitBytes configures the Go GC soft memory ceiling inside _worker (128 MiB).
	DefaultWorkerMemoryLimitBytes int64 = 128 * 1024 * 1024

	// DefaultWorkerHeapDeltaBytes limits active Go heap growth inside _worker VM execution (64 MiB).
	DefaultWorkerHeapDeltaBytes uint64 = 64 * 1024 * 1024
)

// RunWorker executes a single framed WorkerRequest from stdin and writes one
// compact NDJSON WorkerResponse frame to stdout.
func RunWorker(stdin io.Reader, stdout io.Writer) int {
	applyWorkerRLimits()
	debug.SetMemoryLimit(DefaultWorkerMemoryLimitBytes)

	req, err := decodeWorkerRequest(stdin)
	if err != nil {
		return writeWorkerResponse(stdout, contract.WorkerResponse{
			OK:    false,
			Error: asToolError(err),
		})
	}

	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = contract.DefaultTimeoutMs
	}
	if timeoutMs > contract.MaxTimeoutMs {
		timeoutMs = contract.MaxTimeoutMs
	}

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), workerTerminationSignals()...)
	defer stopSignals()

	ctx, cancel := context.WithTimeout(sigCtx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	resp := ExecuteInProcessWithConfig(ctx, req, ProcessConfig{
		MaxHeapDeltaBytes: DefaultWorkerHeapDeltaBytes,
	})
	return writeWorkerResponse(stdout, resp)
}

func writeWorkerResponse(stdout io.Writer, resp contract.WorkerResponse) int {
	frame, err := encodeIPCFrame(resp, "worker response")
	if err != nil {
		fallback := contract.WorkerResponse{
			OK:          false,
			Error:       asToolError(err),
			Diagnostics: resp.Diagnostics,
			DurationMs:  resp.DurationMs,
		}
		var fbErr error
		frame, fbErr = encodeIPCFrame(fallback, "worker error response")
		if fbErr != nil {
			frame = []byte(fmt.Sprintf(`{"ok":false,"error":{"code":"INTERNAL_ERROR","message":%q},"duration_ms":0}`+"\n", fbErr.Error()))
		}
	}
	if _, writeErr := stdout.Write(frame); writeErr != nil {
		return 1
	}
	return 0
}
