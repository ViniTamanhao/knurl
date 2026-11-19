package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	// MaxIPCFrameBytes bounds each single-shot WorkerRequest/WorkerResponse JSON frame (2 MiB).
	MaxIPCFrameBytes = 2 * 1024 * 1024

	// MaxWorkerStderrBytes bounds captured diagnostic stderr from a _worker subprocess (64 KiB).
	MaxWorkerStderrBytes = 64 * 1024
)

func encodeIPCFrame(v any, label string) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("marshal %s: %v", label, err),
		}
	}
	frame := make([]byte, len(raw)+1)
	copy(frame, raw)
	frame[len(raw)] = '\n'
	if len(frame) > MaxIPCFrameBytes {
		return nil, &contract.ToolError{
			Code:    contract.ErrLimitExceeded,
			Message: fmt.Sprintf("%s frame size %d exceeds %d byte IPC cap", label, len(frame), MaxIPCFrameBytes),
		}
	}
	return frame, nil
}

func decodeWorkerRequest(r io.Reader) (contract.WorkerRequest, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxIPCFrameBytes+1))
	if err != nil {
		return contract.WorkerRequest{}, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("read worker request: %v", err),
		}
	}
	if len(data) > MaxIPCFrameBytes {
		return contract.WorkerRequest{}, &contract.ToolError{
			Code:    contract.ErrLimitExceeded,
			Message: fmt.Sprintf("worker request exceeded %d byte IPC cap", MaxIPCFrameBytes),
		}
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return contract.WorkerRequest{}, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: "empty worker request frame",
		}
	}
	var req contract.WorkerRequest
	if unmarshalErr := json.Unmarshal(trimmed, &req); unmarshalErr != nil {
		return contract.WorkerRequest{}, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("decode worker request: %v", unmarshalErr),
		}
	}
	return req, nil
}

func decodeWorkerResponse(data []byte) (contract.WorkerResponse, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return contract.WorkerResponse{}, fmt.Errorf("empty worker response frame")
	}
	var resp contract.WorkerResponse
	if err := json.Unmarshal(trimmed, &resp); err != nil {
		return contract.WorkerResponse{}, fmt.Errorf("decode worker response: %w", err)
	}
	return resp, nil
}
