package host

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

const (
	maxDiagnosticsLines = 256
	maxDiagnosticBytes  = 64 * 1024
)

// Option configures optional dependencies or test hooks on a Host instance.
type Option func(*Host)

// WithHTTPTransport overrides the HTTP RoundTripper used by knurl.http while
// preserving all manifest allowlist, secret injection, redirect, and byte-cap checks.
func WithHTTPTransport(rt http.RoundTripper) Option {
	return func(h *Host) {
		if rt != nil {
			h.httpTransport = rt
		}
	}
}

// WithHTTPLookupIP overrides DNS resolution inside the SSRF-safe HTTP dialer.
func WithHTTPLookupIP(fn func(context.Context, string) ([]net.IPAddr, error)) Option {
	return func(h *Host) {
		if fn != nil {
			h.httpLookupIP = fn
		}
	}
}

// Host provides capability-checked OS primitives for one tool execution.
type Host struct {
	mocks           *contract.TestMocks
	manifest        contract.Manifest
	workDir         string
	root            *os.Root
	secrets         map[string]string
	declaredSecrets map[string]struct{}
	redactor        *Redactor
	envLookup       func(string) (string, bool)
	httpTransport   http.RoundTripper
	ownedTransport  *http.Transport
	httpLookupIP    func(context.Context, string) ([]net.IPAddr, error)
	diagnostics     []string
	diagBytes       int
}

// WithMocks selects fixture-only host operations; absent mocks never fall through.
func WithMocks(mocks *contract.TestMocks) Option {
	return func(h *Host) { h.mocks = mocks }
}

// New constructs a Host confined to workDir for the supplied manifest and secrets.
func New(
	manifest contract.Manifest,
	workDir string,
	secrets map[string]string,
	redactor *Redactor,
) (*Host, error) {
	return NewWithOptions(manifest, workDir, secrets, redactor)
}

// NewWithOptions constructs a Host confined to workDir with optional host hooks.
func NewWithOptions(
	manifest contract.Manifest,
	workDir string,
	secrets map[string]string,
	redactor *Redactor,
	opts ...Option,
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
	h := &Host{
		manifest:        manifest,
		workDir:         workDir,
		root:            root,
		secrets:         secrets,
		declaredSecrets: declared,
		redactor:        redactor,
		envLookup:       os.LookupEnv,
		httpLookupIP:    net.DefaultResolver.LookupIPAddr,
		diagnostics:     make([]string, 0),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	if h.httpTransport == nil {
		tr := &http.Transport{
			Proxy:               nil,
			DialContext:         h.ssrfSafeDialContext,
			TLSHandshakeTimeout: 5 * time.Second,
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
		}
		h.ownedTransport = tr
		h.httpTransport = tr
	}
	return h, nil
}

// Close releases the underlying os.Root file descriptor and idle HTTP connections.
func (h *Host) Close() error {
	if h.ownedTransport != nil {
		h.ownedTransport.CloseIdleConnections()
	}
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
