package runtime

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

// RunWorker executes a single framed WorkerRequest from stdin and writes one
// compact NDJSON WorkerResponse frame to stdout.
func RunWorker(stdin io.Reader, stdout io.Writer) int {
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

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	resp := ExecuteInProcess(ctx, req)
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
