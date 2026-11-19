package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/mcpserver"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

const runHelpText = `Usage:
  knurl run <tool-name-or-path> [flags]

Flags:
  --input <json>              JSON object input passed to main(input)
  --arg <key=value>           Set a typed input field (repeatable)
  --scope <auto|repo|global>  Registry scope to resolve the tool from (default auto)
  --version <N|vN>            Explicit tool version to execute
  --tools-dir <dir>           Override registry directory (default $KNURL_TOOLS_DIR or two-tier lookup)
  --workdir <dir>             Workspace directory exposed to tool execution (default current directory)
  --timeout-ms <ms>           Override execution timeout in milliseconds (100..120000)
  --json                      Print full ExecutionResult JSON envelope
  -h, --help                  Show help for knurl run
`

const mcpHelpText = `Usage:
  knurl mcp [flags]

Flags:
  --tools-dir <dir>           Override registry directory (default $KNURL_TOOLS_DIR or two-tier lookup)
  --workdir <dir>             Workspace directory exposed to tool execution (default current directory)
  -h, --help                  Show help for knurl mcp
`

type runCmdOptions struct {
	target    string
	inputJSON string
	hasInput  bool
	rawArgs   []string
	scope     contract.Scope
	version   int
	toolsDir  string
	workDir   string
	timeoutMs int
	jsonOut   bool
	help      bool
}

func defaultToolsDir() string {
	return os.Getenv("KNURL_TOOLS_DIR")
}

func runToolCmd(args []string, stdout, stderr io.Writer) int {
	opts, err := parseRunArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "knurl run: %v\n", err)
		return 2
	}
	if opts.help {
		return writeOut(stdout, runHelpText, stderr)
	}
	if opts.target == "" {
		_, _ = fmt.Fprintln(stderr, "knurl run: missing <tool-name-or-path> (run 'knurl run --help')")
		return 2
	}

	toolsDir := opts.toolsDir
	if toolsDir == "" {
		toolsDir = defaultToolsDir()
	}
	workDir := opts.workDir
	if workDir == "" {
		var wdErr error
		workDir, wdErr = os.Getwd()
		if wdErr != nil {
			_, _ = fmt.Fprintf(stderr, "knurl run: resolve working directory: %v\n", wdErr)
			return 1
		}
	}

	scope := opts.scope
	if scope == "" {
		scope = contract.ScopeAuto
	}

	reg := &registry.Registry{
		WorkDir:     workDir,
		OverrideDir: toolsDir,
	}
	bundle, loadErr := reg.LoadTarget(opts.target, scope, opts.version)
	if loadErr != nil {
		return emitFailure(stderr, opts.target, opts.version, scope, asToolError(loadErr), opts.jsonOut)
	}

	inputMap, buildErr := buildRunInput(bundle.Manifest, opts)
	if buildErr != nil {
		return emitFailure(stderr, bundle.Name, bundle.Version, bundle.Scope, asToolError(buildErr), opts.jsonOut)
	}

	sup := &runtime.Supervisor{}
	res := sup.ExecuteBundle(
		context.Background(),
		bundle.Manifest,
		bundle.Source,
		workDir,
		inputMap,
		opts.timeoutMs,
		bundle.Scope,
	)

	for _, line := range res.Diagnostics {
		_, _ = fmt.Fprintf(stderr, "[knurl] %s\n", line)
	}

	if !res.OK {
		if opts.jsonOut {
			_ = writeJSONLine(stderr, res, stderr)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "knurl: %s\n", res.Error.Error())
		return 1
	}

	if opts.jsonOut {
		return writeJSONLine(stdout, res, stderr)
	}
	return writeJSONLine(stdout, res.Output, stderr)
}

func runMCPCmd(args []string, stdout, stderr io.Writer) int {
	toolsDir := defaultToolsDir()
	workDir := ""

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return writeOut(stdout, mcpHelpText, stderr)
		case arg == "--tools-dir":
			val, nextIdx, err := nextFlagValue(args, i, "--tools-dir")
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "knurl mcp: %v\n", err)
				return 2
			}
			toolsDir = val
			i = nextIdx
		case strings.HasPrefix(arg, "--tools-dir="):
			toolsDir = strings.TrimPrefix(arg, "--tools-dir=")
		case arg == "--workdir":
			val, nextIdx, err := nextFlagValue(args, i, "--workdir")
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "knurl mcp: %v\n", err)
				return 2
			}
			workDir = val
			i = nextIdx
		case strings.HasPrefix(arg, "--workdir="):
			workDir = strings.TrimPrefix(arg, "--workdir=")
		case strings.HasPrefix(arg, "-"):
			_, _ = fmt.Fprintf(stderr, "knurl mcp: unknown flag %q\n", arg)
			return 2
		default:
			_, _ = fmt.Fprintf(stderr, "knurl mcp: unexpected positional argument %q\n", arg)
			return 2
		}
	}

	if workDir == "" {
		var wdErr error
		workDir, wdErr = os.Getwd()
		if wdErr != nil {
			_, _ = fmt.Fprintf(stderr, "knurl mcp: resolve working directory: %v\n", wdErr)
			return 1
		}
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
	server := mcpserver.NewServer(mcpserver.Config{
		Registry: &registry.Registry{
			WorkDir:     workDir,
			OverrideDir: toolsDir,
		},
		Supervisor: &runtime.Supervisor{},
		WorkDir:    workDir,
		Version:    version,
		Logger:     logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "knurl mcp: %v\n", err)
		return 1
	}
	return 0
}

func parseRunArgs(args []string) (runCmdOptions, error) {
	var opts runCmdOptions
	endOfFlags := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !endOfFlags && arg == "--" {
			endOfFlags = true
			continue
		}
		if !endOfFlags && strings.HasPrefix(arg, "-") {
			switch {
			case arg == "-h" || arg == "--help":
				opts.help = true
				return opts, nil
			case arg == "--json":
				opts.jsonOut = true
			case arg == "--input":
				val, nextIdx, err := nextFlagValue(args, i, "--input")
				if err != nil {
					return opts, err
				}
				opts.inputJSON = val
				opts.hasInput = true
				i = nextIdx
			case strings.HasPrefix(arg, "--input="):
				opts.inputJSON = strings.TrimPrefix(arg, "--input=")
				opts.hasInput = true
			case arg == "--arg":
				val, nextIdx, err := nextFlagValue(args, i, "--arg")
				if err != nil {
					return opts, err
				}
				opts.rawArgs = append(opts.rawArgs, val)
				i = nextIdx
			case strings.HasPrefix(arg, "--arg="):
				opts.rawArgs = append(opts.rawArgs, strings.TrimPrefix(arg, "--arg="))
			case arg == "--scope":
				val, nextIdx, err := nextFlagValue(args, i, "--scope")
				if err != nil {
					return opts, err
				}
				parsedScope, scopeErr := parseScopeFlag(val)
				if scopeErr != nil {
					return opts, scopeErr
				}
				opts.scope = parsedScope
				i = nextIdx
			case strings.HasPrefix(arg, "--scope="):
				parsedScope, scopeErr := parseScopeFlag(strings.TrimPrefix(arg, "--scope="))
				if scopeErr != nil {
					return opts, scopeErr
				}
				opts.scope = parsedScope
			case arg == "--version":
				val, nextIdx, err := nextFlagValue(args, i, "--version")
				if err != nil {
					return opts, err
				}
				parsedVer, verErr := parseVersionFlag(val)
				if verErr != nil {
					return opts, verErr
				}
				opts.version = parsedVer
				i = nextIdx
			case strings.HasPrefix(arg, "--version="):
				parsedVer, verErr := parseVersionFlag(strings.TrimPrefix(arg, "--version="))
				if verErr != nil {
					return opts, verErr
				}
				opts.version = parsedVer
			case arg == "--tools-dir":
				val, nextIdx, err := nextFlagValue(args, i, "--tools-dir")
				if err != nil {
					return opts, err
				}
				opts.toolsDir = val
				i = nextIdx
			case strings.HasPrefix(arg, "--tools-dir="):
				opts.toolsDir = strings.TrimPrefix(arg, "--tools-dir=")
			case arg == "--workdir":
				val, nextIdx, err := nextFlagValue(args, i, "--workdir")
				if err != nil {
					return opts, err
				}
				opts.workDir = val
				i = nextIdx
			case strings.HasPrefix(arg, "--workdir="):
				opts.workDir = strings.TrimPrefix(arg, "--workdir=")
			case arg == "--timeout-ms":
				val, nextIdx, err := nextFlagValue(args, i, "--timeout-ms")
				if err != nil {
					return opts, err
				}
				parsed, parseErr := parseTimeoutFlag(val)
				if parseErr != nil {
					return opts, parseErr
				}
				opts.timeoutMs = parsed
				i = nextIdx
			case strings.HasPrefix(arg, "--timeout-ms="):
				parsed, parseErr := parseTimeoutFlag(strings.TrimPrefix(arg, "--timeout-ms="))
				if parseErr != nil {
					return opts, parseErr
				}
				opts.timeoutMs = parsed
			default:
				return opts, fmt.Errorf("unknown flag %q", arg)
			}
			continue
		}

		if opts.target != "" {
			return opts, fmt.Errorf("unexpected extra argument %q", arg)
		}
		opts.target = arg
	}
	return opts, nil
}

func nextFlagValue(args []string, idx int, flagName string) (string, int, error) {
	if idx+1 >= len(args) {
		return "", idx, fmt.Errorf("flag %s requires a value", flagName)
	}
	return args[idx+1], idx + 1, nil
}

func parseTimeoutFlag(raw string) (int, error) {
	ms, err := strconv.Atoi(raw)
	if err != nil || ms < 100 || ms > contract.MaxTimeoutMs {
		return 0, fmt.Errorf("invalid --timeout-ms %q (expected integer 100..%d)", raw, contract.MaxTimeoutMs)
	}
	return ms, nil
}

func parseScopeFlag(raw string) (contract.Scope, error) {
	switch contract.Scope(raw) {
	case contract.ScopeAuto, contract.ScopeRepo, contract.ScopeGlobal:
		return contract.Scope(raw), nil
	default:
		return "", fmt.Errorf("invalid --scope %q (expected auto, repo, or global)", raw)
	}
}

func parseVersionFlag(raw string) (int, error) {
	s := strings.TrimPrefix(raw, "v")
	if s == "" || strings.HasPrefix(s, "0") {
		return 0, fmt.Errorf("invalid --version %q (expected positive integer N or vN)", raw)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("invalid --version %q (expected positive integer N or vN)", raw)
		}
	}
	ver, err := strconv.Atoi(s)
	if err != nil || ver < 1 {
		return 0, fmt.Errorf("invalid --version %q (expected positive integer N or vN)", raw)
	}
	return ver, nil
}

func buildRunInput(manifest contract.Manifest, opts runCmdOptions) (map[string]any, error) {
	input := make(map[string]any)
	if opts.hasInput {
		if err := json.Unmarshal([]byte(opts.inputJSON), &input); err != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidInput,
				Message: fmt.Sprintf("parse --input JSON object: %v", err),
			}
		}
		if input == nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidInput,
				Message: "--input must be a JSON object, got null",
			}
		}
	}

	props, _ := manifest.InputSchema["properties"].(map[string]any)
	for _, rawArg := range opts.rawArgs {
		key, val, ok := strings.Cut(rawArg, "=")
		if !ok || key == "" {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidInput,
				Message: fmt.Sprintf("invalid --arg %q: expected key=value", rawArg),
			}
		}
		coerced, coerceErr := coerceArgValue(props, key, val)
		if coerceErr != nil {
			return nil, coerceErr
		}
		input[key] = coerced
	}
	return input, nil
}

func coerceArgValue(props map[string]any, key, rawVal string) (any, error) {
	if props == nil {
		return rawVal, nil
	}
	propMap, ok := props[key].(map[string]any)
	if !ok {
		return rawVal, nil
	}

	var typ string
	switch t := propMap["type"].(type) {
	case string:
		typ = t
	case []any:
		for _, item := range t {
			if s, isStr := item.(string); isStr && s != "null" {
				typ = s
				break
			}
		}
		if rawVal == "null" {
			return nil, nil
		}
	}

	switch typ {
	case "boolean":
		b, err := strconv.ParseBool(rawVal)
		if err != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidInput,
				Message: fmt.Sprintf("invalid boolean value %q for --arg %s", rawVal, key),
			}
		}
		return b, nil
	case "integer":
		n, err := strconv.ParseInt(rawVal, 10, 64)
		if err != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidInput,
				Message: fmt.Sprintf("invalid integer value %q for --arg %s", rawVal, key),
			}
		}
		return n, nil
	case "number":
		f, err := strconv.ParseFloat(rawVal, 64)
		if err != nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidInput,
				Message: fmt.Sprintf("invalid number value %q for --arg %s", rawVal, key),
			}
		}
		return f, nil
	case "array":
		if strings.HasPrefix(strings.TrimSpace(rawVal), "[") {
			var arr []any
			if err := json.Unmarshal([]byte(rawVal), &arr); err != nil {
				return nil, &contract.ToolError{
					Code:    contract.ErrInvalidInput,
					Message: fmt.Sprintf("invalid JSON array %q for --arg %s: %v", rawVal, key, err),
				}
			}
			return arr, nil
		}
		if rawVal == "" {
			return []string{}, nil
		}
		return strings.Split(rawVal, ","), nil
	case "object":
		var obj map[string]any
		if err := json.Unmarshal([]byte(rawVal), &obj); err != nil || obj == nil {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidInput,
				Message: fmt.Sprintf("invalid JSON object %q for --arg %s", rawVal, key),
			}
		}
		return obj, nil
	default:
		return rawVal, nil
	}
}

func emitFailure(w io.Writer, tool string, versionNum int, scope contract.Scope, toolErr *contract.ToolError, jsonOut bool) int {
	if jsonOut {
		_ = writeJSONLine(w, contract.ExecutionResult{
			OK:      false,
			Tool:    tool,
			Version: versionNum,
			Scope:   scope,
			Error:   toolErr,
		}, w)
		return 1
	}
	_, _ = fmt.Fprintf(w, "knurl: %s\n", toolErr.Error())
	return 1
}

func writeJSONLine(w io.Writer, v any, stderr io.Writer) int {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "knurl: encode output JSON: %v\n", err)
		return 1
	}
	encoded = append(encoded, '\n')
	if _, writeErr := w.Write(encoded); writeErr != nil {
		_, _ = fmt.Fprintf(stderr, "knurl: write output: %v\n", writeErr)
		return 1
	}
	return 0
}

func asToolError(err error) *contract.ToolError {
	if err == nil {
		return nil
	}
	var toolErr *contract.ToolError
	if errors.As(err, &toolErr) && toolErr != nil {
		return toolErr
	}
	return &contract.ToolError{
		Code:    contract.ErrInternalError,
		Message: err.Error(),
	}
}
