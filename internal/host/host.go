package host

import (
	"fmt"
	"os"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	maxDiagnosticsLines = 256
	maxDiagnosticBytes  = 64 * 1024
)

// Host provides capability-checked OS primitives for one tool execution.
type Host struct {
	manifest        contract.Manifest
	workDir         string
	root            *os.Root
	secrets         map[string]string
	declaredSecrets map[string]struct{}
	redactor        *Redactor
	envLookup       func(string) (string, bool)
	diagnostics     []string
	diagBytes       int
}

// New constructs a Host confined to workDir for the supplied manifest and secrets.
func New(
	manifest contract.Manifest,
	workDir string,
	secrets map[string]string,
	redactor *Redactor,
) (*Host, error) {
	if workDir == "" {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "work_dir must not be empty",
		}
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("open workspace root %q: %v", workDir, err),
		}
	}
	declared := make(map[string]struct{}, len(manifest.Secrets))
	for _, ref := range manifest.Secrets {
		declared[ref.Name] = struct{}{}
	}
	if redactor == nil {
		redactor = NewRedactor(secrets)
	}
	return &Host{
		manifest:        manifest,
		workDir:         workDir,
		root:            root,
		secrets:         secrets,
		declaredSecrets: declared,
		redactor:        redactor,
		envLookup:       os.LookupEnv,
		diagnostics:     make([]string, 0),
	}, nil
}

// Close releases the underlying os.Root file descriptor.
func (h *Host) Close() error {
	if h.root != nil {
		return h.root.Close()
	}
	return nil
}

// SecretsHas reports whether a declared secret has a non-empty value on the host.
// Calling SecretsHas on an undeclared secret returns ErrCapabilityDenied.
func (h *Host) SecretsHas(name string) (bool, error) {
	if _, declared := h.declaredSecrets[name]; !declared {
		return false, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("secret %q is not declared in manifest.secrets", name),
		}
	}
	val, ok := h.secrets[name]
	return ok && val != "", nil
}

// Log appends a redacted diagnostic line up to bounded diagnostics limits.
func (h *Host) Log(msg string) {
	if len(h.diagnostics) >= maxDiagnosticsLines || h.diagBytes >= maxDiagnosticBytes {
		return
	}
	scrubbed := h.redactor.String(msg)
	remaining := maxDiagnosticBytes - h.diagBytes
	if len(scrubbed) > remaining {
		scrubbed = scrubbed[:remaining]
	}
	h.diagnostics = append(h.diagnostics, scrubbed)
	h.diagBytes += len(scrubbed)
}

// Diagnostics returns a copy of collected redacted diagnostic messages.
func (h *Host) Diagnostics() []string {
	if len(h.diagnostics) == 0 {
		return nil
	}
	out := make([]string, len(h.diagnostics))
	copy(out, h.diagnostics)
	return out
}
