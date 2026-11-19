package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

// MaxExecStreamBytes bounds captured stdout and stderr per exec call (256 KiB).
const MaxExecStreamBytes = 262144

var (
	execProgramRegexp = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

	forbiddenShellPrograms = map[string]struct{}{
		"sh":             {},
		"bash":           {},
		"zsh":            {},
		"dash":           {},
		"fish":           {},
		"ksh":            {},
		"csh":            {},
		"tcsh":           {},
		"cmd":            {},
		"cmd.exe":        {},
		"powershell":     {},
		"powershell.exe": {},
		"pwsh":           {},
		"eval":           {},
	}

	forbiddenDynamicLinkerEnvKeys = map[string]struct{}{
		"LD_PRELOAD":            {},
		"LD_LIBRARY_PATH":       {},
		"DYLD_INSERT_LIBRARIES": {},
		"DYLD_LIBRARY_PATH":     {},
		"DYLD_FRAMEWORK_PATH":   {},
	}
)

// ExecOptions configures knurl.exec.
type ExecOptions struct {
	TimeoutMs  int
	HasTimeout bool
}

// ExecResult matches the knurl.exec JS return contract.
type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Truncated  bool   `json:"truncated"`
	DurationMs int64  `json:"duration_ms"`
}

// Exec executes an approved ExecTemplate directly via argv without a shell.
func (h *Host) Exec(ctx context.Context, templateID string, params map[string]string, opts ExecOptions) (ExecResult, error) {
	tmpl, err := h.lookupExecTemplate(templateID)
	if err != nil {
		return ExecResult{}, err
	}
	if validateErr := validateExecParams(tmpl, params); validateErr != nil {
		return ExecResult{}, validateErr
	}

	expandedArgs := make([]string, len(tmpl.Args))
	for i, rawArg := range tmpl.Args {
		expanded, expErr := h.expandTemplateString(rawArg, params, true)
		if expErr != nil {
			return ExecResult{}, expErr
		}
		expandedArgs[i] = expanded
	}

	env := SanitizedBaseEnv(h.envLookup)
	for k, rawVal := range tmpl.Env {
		expandedVal, expErr := h.expandTemplateString(rawVal, nil, false)
		if expErr != nil {
			return ExecResult{}, expErr
		}
		env = append(env, k+"="+expandedVal)
	}

	execCtx := ctx
	var cancel context.CancelFunc
	if opts.HasTimeout {
		if opts.TimeoutMs <= 0 {
			return ExecResult{}, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("exec timeout_ms must be positive, got %d", opts.TimeoutMs),
			}
		}
		timeoutMs := min(opts.TimeoutMs, contract.MaxTimeoutMs)
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
		defer cancel()
	}

	start := time.Now()
	//nolint:gosec // G204: program and argv strictly validated against approved ExecTemplate.
	cmd := exec.CommandContext(execCtx, tmpl.Program, expandedArgs...)
	cmd.Dir = h.workDir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(nil)
	ConfigureProcessGroup(cmd)

	stdout := NewLimitedBuffer(MaxExecStreamBytes)
	stderr := NewLimitedBuffer(MaxExecStreamBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	durationMs := time.Since(start).Milliseconds()

	if runErr != nil {
		if execCtx.Err() != nil {
			return ExecResult{}, &contract.ToolError{
				Code:    contract.ErrTimeout,
				Message: fmt.Sprintf("exec template %q timed out", templateID),
			}
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return ExecResult{
				ExitCode:   exitErr.ExitCode(),
				Stdout:     h.redactor.String(stdout.String()),
				Stderr:     h.redactor.String(stderr.String()),
				Truncated:  stdout.Truncated() || stderr.Truncated(),
				DurationMs: durationMs,
			}, nil
		}
		return ExecResult{}, &contract.ToolError{
			Code:    contract.ErrExecFailed,
			Message: h.redactor.String(fmt.Sprintf("exec template %q failed to start %q: %v", templateID, tmpl.Program, runErr)),
		}
	}

	return ExecResult{
		ExitCode:   0,
		Stdout:     h.redactor.String(stdout.String()),
		Stderr:     h.redactor.String(stderr.String()),
		Truncated:  stdout.Truncated() || stderr.Truncated(),
		DurationMs: durationMs,
	}, nil
}

func (h *Host) lookupExecTemplate(templateID string) (contract.ExecTemplate, error) {
	if h.manifest.Capabilities.Exec == nil {
		return contract.ExecTemplate{}, &contract.ToolError{
			Code:    contract.ErrCapabilityDenied,
			Message: fmt.Sprintf("exec template %q is not granted", templateID),
		}
	}
	for _, tmpl := range h.manifest.Capabilities.Exec.Templates {
		if tmpl.ID != templateID {
			continue
		}
		if !execProgramRegexp.MatchString(tmpl.Program) {
			return contract.ExecTemplate{}, &contract.ToolError{
				Code:    contract.ErrCapabilityDenied,
				Message: fmt.Sprintf("exec template %q invalid program name %q", templateID, tmpl.Program),
			}
		}
		progLower := strings.ToLower(tmpl.Program)
		if _, forbidden := forbiddenShellPrograms[progLower]; forbidden {
			return contract.ExecTemplate{}, &contract.ToolError{
				Code:    contract.ErrCapabilityDenied,
				Message: fmt.Sprintf("forbidden shell program %q in template %q", tmpl.Program, templateID),
			}
		}
		if len(tmpl.Args) == 0 || tmpl.Args[0] == "" || strings.Contains(tmpl.Args[0], "${") {
			return contract.ExecTemplate{}, &contract.ToolError{
				Code:    contract.ErrCapabilityDenied,
				Message: fmt.Sprintf("exec template %q violates program/args[0] invariants", templateID),
			}
		}
		for envKey := range tmpl.Env {
			if _, blocked := forbiddenDynamicLinkerEnvKeys[strings.ToUpper(envKey)]; blocked {
				return contract.ExecTemplate{}, &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: fmt.Sprintf("exec template %q sets forbidden dynamic linker env variable %q", templateID, envKey),
				}
			}
		}
		return tmpl, nil
	}
	return contract.ExecTemplate{}, &contract.ToolError{
		Code:    contract.ErrCapabilityDenied,
		Message: fmt.Sprintf("exec template %q is not declared in manifest", templateID),
	}
}

func validateExecParams(tmpl contract.ExecTemplate, params map[string]string) error {
	for idx, rawArg := range tmpl.Args {
		rest := rawArg
		for {
			start := strings.Index(rest, "${")
			if start < 0 {
				break
			}
			after := rest[start+2:]
			end := strings.IndexByte(after, '}')
			if end < 0 {
				return &contract.ToolError{
					Code:    contract.ErrInvalidManifest,
					Message: fmt.Sprintf("exec template %q args[%d] contains unclosed placeholder", tmpl.ID, idx),
				}
			}
			token := after[:end]
			if argKey, ok := strings.CutPrefix(token, "arg:"); ok {
				if _, declared := tmpl.ArgsSchema[argKey]; !declared {
					return &contract.ToolError{
						Code:    contract.ErrInvalidManifest,
						Message: fmt.Sprintf("exec template %q args[%d] references undeclared arg %q", tmpl.ID, idx, argKey),
					}
				}
			}
			rest = after[end+1:]
		}
	}

	for k := range params {
		if _, ok := tmpl.ArgsSchema[k]; !ok {
			return &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("exec template %q does not accept argument %q", tmpl.ID, k),
			}
		}
	}
	for key, spec := range tmpl.ArgsSchema {
		val, ok := params[key]
		if !ok {
			return &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("exec template %q missing required argument %q", tmpl.ID, key),
			}
		}
		if strings.ContainsRune(val, 0) {
			return &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("exec argument %q contains NUL byte", key),
			}
		}
		if spec.MaxLength > 0 && len(val) > spec.MaxLength {
			return &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("exec argument %q exceeds max_length %d", key, spec.MaxLength),
			}
		}
		if len(spec.Enum) > 0 && !slices.Contains(spec.Enum, val) {
			return &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("exec argument %q value %q is not in allowed enum", key, val),
			}
		}
		re, err := regexp.Compile(`\A(?:` + spec.Pattern + `)\z`)
		if err != nil {
			return &contract.ToolError{
				Code:    contract.ErrInvalidManifest,
				Message: fmt.Sprintf("exec argument %q invalid pattern: %v", key, err),
			}
		}
		if !re.MatchString(val) {
			return &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("exec argument %q failed pattern validation", key),
			}
		}
	}
	return nil
}

// expandTemplateString performs a single left-to-right pass replacing ${arg:...}
// and ${secret:...} placeholders without re-scanning substituted values.
func (h *Host) expandTemplateString(raw string, params map[string]string, allowArgs bool) (string, error) {
	var b strings.Builder
	pos := 0
	for pos < len(raw) {
		relStart := strings.Index(raw[pos:], "${")
		if relStart < 0 {
			b.WriteString(raw[pos:])
			break
		}
		start := pos + relStart
		b.WriteString(raw[pos:start])

		afterStart := start + 2
		relEnd := strings.IndexByte(raw[afterStart:], '}')
		if relEnd < 0 {
			return "", &contract.ToolError{
				Code:    contract.ErrInvalidManifest,
				Message: fmt.Sprintf("unclosed placeholder in template %q", raw),
			}
		}
		token := raw[afterStart : afterStart+relEnd]
		switch {
		case strings.HasPrefix(token, "arg:"):
			if !allowArgs {
				return "", &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: "${arg:...} placeholders are not permitted in template env",
				}
			}
			key := strings.TrimPrefix(token, "arg:")
			val, ok := params[key]
			if !ok {
				return "", &contract.ToolError{
					Code:    contract.ErrInvalidArgument,
					Message: fmt.Sprintf("missing value for ${arg:%s}", key),
				}
			}
			b.WriteString(val)
		case strings.HasPrefix(token, "secret:"):
			name := strings.TrimPrefix(token, "secret:")
			if _, declared := h.declaredSecrets[name]; !declared {
				return "", &contract.ToolError{
					Code:    contract.ErrCapabilityDenied,
					Message: fmt.Sprintf("secret %q is not declared in manifest.secrets", name),
				}
			}
			secVal, ok := h.secrets[name]
			if !ok || secVal == "" {
				return "", &contract.ToolError{
					Code:    contract.ErrSecretMissing,
					Message: fmt.Sprintf("secret %s is not configured on host", name),
					Details: map[string]any{"secret": name},
				}
			}
			b.WriteString(secVal)
		default:
			return "", &contract.ToolError{
				Code:    contract.ErrInvalidManifest,
				Message: fmt.Sprintf("unsupported placeholder ${%s}", token),
			}
		}
		pos = afterStart + relEnd + 1
	}
	return b.String(), nil
}
