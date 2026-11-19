package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/host"
)

// Supervisor orchestrates input/output schema validation, host secret
// resolution, and isolated `knurl _worker` subprocess execution.
type Supervisor struct {
	Executable string
	EnvLookup  func(string) (string, bool)
}

func (s *Supervisor) lookupEnv() func(string) (string, bool) {
	if s != nil && s.EnvLookup != nil {
		return s.EnvLookup
	}
	return os.LookupEnv
}

func (s *Supervisor) resolveExecutable() (string, error) {
	if s != nil && s.Executable != "" {
		return s.Executable, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("resolve executable path: %v", err),
		}
	}
	return exe, nil
}

// ExecuteBundle validates inputs against manifest.InputSchema, resolves
// declared secrets, executes tool.js inside an isolated _worker subprocess,
// and validates returned outputs against manifest.OutputSchema.
func (s *Supervisor) ExecuteBundle(
	ctx context.Context,
	manifest contract.Manifest,
	source string,
	workDir string,
	input map[string]any,
	timeoutMs int,
	scope contract.Scope,
) contract.ExecutionResult {
	start := time.Now()
	if scope == "" {
		scope = contract.ScopeRepo
	}
	fail := func(err *contract.ToolError, diagnostics []string) contract.ExecutionResult {
		return contract.ExecutionResult{
			OK:          false,
			Tool:        manifest.Name,
			Version:     manifest.Version,
			Scope:       scope,
			DurationMs:  time.Since(start).Milliseconds(),
			Error:       err,
			Diagnostics: diagnostics,
		}
	}

	if err := contract.ValidateInput(manifest, input); err != nil {
		return fail(asToolError(err), nil)
	}

	secrets, err := host.ResolveSecrets(manifest.Secrets, s.lookupEnv())
	if err != nil {
		return fail(asToolError(err), nil)
	}
	redactor := host.NewRedactor(secrets)

	cleanWorkDir, err := validateWorkDir(workDir)
	if err != nil {
		return fail(redactor.ToolError(asToolError(err)), nil)
	}

	effTimeout := manifest.EffectiveTimeoutMs()
	if timeoutMs > 0 {
		effTimeout = min(timeoutMs, contract.MaxTimeoutMs)
	}

	reqInput := input
	if reqInput == nil {
		reqInput = map[string]any{}
	}
	resp := s.RunWorkerRequest(ctx, contract.WorkerRequest{
		Manifest:  manifest,
		Source:    source,
		WorkDir:   cleanWorkDir,
		Input:     reqInput,
		TimeoutMs: effTimeout,
		Secrets:   secrets,
	})
	if !resp.OK {
		return contract.ExecutionResult{
			OK:          false,
			Tool:        manifest.Name,
			Version:     manifest.Version,
			Scope:       scope,
			DurationMs:  time.Since(start).Milliseconds(),
			Error:       redactor.ToolError(resp.Error),
			Diagnostics: resp.Diagnostics,
		}
	}

	if valErr := contract.ValidateOutput(manifest, resp.Output); valErr != nil {
		return fail(redactor.ToolError(asToolError(valErr)), resp.Diagnostics)
	}

	return contract.ExecutionResult{
		OK:          true,
		Tool:        manifest.Name,
		Version:     manifest.Version,
		Scope:       scope,
		DurationMs:  time.Since(start).Milliseconds(),
		Output:      resp.Output,
		Diagnostics: resp.Diagnostics,
	}
}

// RunWorkerRequest spawns an isolated `knurl _worker` subprocess with a
// sanitized environment and enforces timeout, IPC size caps, and redaction.
func (s *Supervisor) RunWorkerRequest(ctx context.Context, req contract.WorkerRequest) contract.WorkerResponse {
	start := time.Now()
	elapsedMs := func() int64 { return time.Since(start).Milliseconds() }
	redactor := host.NewRedactor(req.Secrets)

	reqBytes, err := encodeIPCFrame(req, "worker request")
	if err != nil {
		return contract.WorkerResponse{
			OK:         false,
			Error:      redactor.ToolError(asToolError(err)),
			DurationMs: elapsedMs(),
		}
	}

	exePath, err := s.resolveExecutable()
	if err != nil {
		return contract.WorkerResponse{
			OK:         false,
			Error:      redactor.ToolError(asToolError(err)),
			DurationMs: elapsedMs(),
		}
	}

	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = contract.DefaultTimeoutMs
	}
	if timeoutMs > contract.MaxTimeoutMs {
		timeoutMs = contract.MaxTimeoutMs
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	//nolint:gosec // G204: spawns knurl self-binary via os.Executable with fixed _worker subcommand.
	cmd := exec.CommandContext(runCtx, exePath, "_worker")
	cmd.Dir = req.WorkDir
	cmd.Env = host.SanitizedBaseEnv(s.lookupEnv())
	cmd.Stdin = bytes.NewReader(reqBytes)
	host.ConfigureProcessGroup(cmd)

	stdoutBuf := host.NewLimitedBuffer(MaxIPCFrameBytes + 1)
	stderrBuf := host.NewLimitedBuffer(MaxWorkerStderrBytes)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	runErr := cmd.Run()

	if runCtx.Err() != nil {
		return contract.WorkerResponse{
			OK: false,
			Error: &contract.ToolError{
				Code:    contract.ErrTimeout,
				Message: fmt.Sprintf("worker execution exceeded %d ms timeout", timeoutMs),
			},
			DurationMs: elapsedMs(),
		}
	}

	if stdoutBuf.Truncated() || len(stdoutBuf.Bytes()) > MaxIPCFrameBytes {
		return contract.WorkerResponse{
			OK: false,
			Error: &contract.ToolError{
				Code:    contract.ErrLimitExceeded,
				Message: fmt.Sprintf("worker stdout frame exceeded %d byte IPC cap", MaxIPCFrameBytes),
			},
			DurationMs: elapsedMs(),
		}
	}

	resp, decErr := decodeWorkerResponse(stdoutBuf.Bytes())
	if decErr != nil {
		stderrMsg := strings.TrimSpace(redactor.String(stderrBuf.String()))
		msg := fmt.Sprintf("worker exited unexpectedly (%v)", decErr)
		if runErr != nil {
			msg = fmt.Sprintf("worker exited unexpectedly (%v: %v)", runErr, decErr)
		}
		if stderrMsg != "" {
			msg = fmt.Sprintf("%s; stderr: %s", msg, stderrMsg)
		}
		return contract.WorkerResponse{
			OK: false,
			Error: redactor.ToolError(&contract.ToolError{
				Code:    contract.ErrWorkerCrashed,
				Message: msg,
			}),
			DurationMs: elapsedMs(),
		}
	}

	redactor.WorkerResponse(&resp)
	return resp
}

func validateWorkDir(workDir string) (string, error) {
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return "", &contract.ToolError{
				Code:    contract.ErrInternalError,
				Message: fmt.Sprintf("resolve working directory: %v", err),
			}
		}
	}
	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return "", &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("resolve working directory %q: %v", workDir, err),
		}
	}
	info, statErr := os.Stat(absDir)
	if statErr != nil || !info.IsDir() {
		return "", &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("working directory %q does not exist or is not a directory", absDir),
		}
	}
	return absDir, nil
}
