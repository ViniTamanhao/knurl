package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/host"
)

var knownHostErrorCodes = map[contract.ErrorCode]struct{}{
	contract.ErrCapabilityDenied: {},
	contract.ErrNotFound:         {},
	contract.ErrNotAGitRepo:      {},
	contract.ErrInvalidArgument:  {},
	contract.ErrSecretMissing:    {},
	contract.ErrTimeout:          {},
	contract.ErrLimitExceeded:    {},
	contract.ErrExecFailed:       {},
	contract.ErrHTTPError:        {},
}

// ExecuteInProcess compiles and runs req inside a fresh synchronous goja VM
// bound to capability-enforced host primitives.
func ExecuteInProcess(ctx context.Context, req contract.WorkerRequest) contract.WorkerResponse {
	start := time.Now()
	elapsedMs := func() int64 { return time.Since(start).Milliseconds() }

	redactor := host.NewRedactor(req.Secrets)
	hostEnv, err := host.New(req.Manifest, req.WorkDir, req.Secrets, redactor)
	if err != nil {
		resp := contract.WorkerResponse{
			OK:         false,
			Error:      asToolError(err),
			DurationMs: elapsedMs(),
		}
		redactor.WorkerResponse(&resp)
		return resp
	}
	defer func() { _ = hostEnv.Close() }()

	vm := goja.New()
	jsonParse, jsonStringify, err := extractJSONBuiltins(vm)
	if err != nil {
		return contract.WorkerResponse{
			OK:         false,
			Error:      asToolError(err),
			DurationMs: elapsedMs(),
		}
	}

	if hyErr := installSandboxHygiene(vm); hyErr != nil {
		return contract.WorkerResponse{
			OK:         false,
			Error:      asToolError(hyErr),
			DurationMs: elapsedMs(),
		}
	}

	if bindErr := bindKnurlHost(ctx, vm, hostEnv, jsonParse, jsonStringify); bindErr != nil {
		return contract.WorkerResponse{
			OK:         false,
			Error:      asToolError(bindErr),
			DurationMs: elapsedMs(),
		}
	}

	prog, compileErr := goja.Compile("tool.js", req.Source, false)
	if compileErr != nil {
		resp := contract.WorkerResponse{
			OK: false,
			Error: &contract.ToolError{
				Code:    contract.ErrCompileError,
				Message: fmt.Sprintf("compile tool.js: %v", compileErr),
			},
			DurationMs: elapsedMs(),
		}
		redactor.WorkerResponse(&resp)
		return resp
	}

	stopWatchdog := make(chan struct{})
	defer close(stopWatchdog)
	go func() {
		select {
		case <-ctx.Done():
			vm.Interrupt(ctx.Err())
		case <-stopWatchdog:
		}
	}()

	if _, runErr := vm.RunProgram(prog); runErr != nil {
		resp := contract.WorkerResponse{
			OK:          false,
			Error:       classifyVMError(ctx, runErr),
			Diagnostics: hostEnv.Diagnostics(),
			DurationMs:  elapsedMs(),
		}
		redactor.WorkerResponse(&resp)
		return resp
	}

	mainFn, ok := goja.AssertFunction(vm.Get("main"))
	if !ok {
		resp := contract.WorkerResponse{
			OK: false,
			Error: &contract.ToolError{
				Code:    contract.ErrCompileError,
				Message: "tool.js must define function main(input)",
			},
			Diagnostics: hostEnv.Diagnostics(),
			DurationMs:  elapsedMs(),
		}
		redactor.WorkerResponse(&resp)
		return resp
	}

	inputMap := req.Input
	if inputMap == nil {
		inputMap = map[string]any{}
	}
	inputVal, inputErr := toNativeJS(vm, jsonParse, inputMap)
	if inputErr != nil {
		resp := contract.WorkerResponse{
			OK:          false,
			Error:       asToolError(inputErr),
			Diagnostics: hostEnv.Diagnostics(),
			DurationMs:  elapsedMs(),
		}
		redactor.WorkerResponse(&resp)
		return resp
	}

	resVal, callErr := mainFn(goja.Undefined(), inputVal)
	if callErr != nil {
		resp := contract.WorkerResponse{
			OK:          false,
			Error:       classifyVMError(ctx, callErr),
			Diagnostics: hostEnv.Diagnostics(),
			DurationMs:  elapsedMs(),
		}
		redactor.WorkerResponse(&resp)
		return resp
	}

	if resVal != nil {
		if _, isPromise := resVal.Export().(*goja.Promise); isPromise {
			resp := contract.WorkerResponse{
				OK: false,
				Error: &contract.ToolError{
					Code:    contract.ErrCompileError,
					Message: "async/await and Promise are not supported in knurl; tools must be synchronous ES6 JavaScript",
				},
				Diagnostics: hostEnv.Diagnostics(),
				DurationMs:  elapsedMs(),
			}
			redactor.WorkerResponse(&resp)
			return resp
		}
	}

	outputMap, outErr := extractJSOutputObject(jsonStringify, resVal)
	if outErr != nil {
		resp := contract.WorkerResponse{
			OK:          false,
			Error:       outErr,
			Diagnostics: hostEnv.Diagnostics(),
			DurationMs:  elapsedMs(),
		}
		redactor.WorkerResponse(&resp)
		return resp
	}

	resp := contract.WorkerResponse{
		OK:          true,
		Output:      outputMap,
		Diagnostics: hostEnv.Diagnostics(),
		DurationMs:  elapsedMs(),
	}
	redactor.WorkerResponse(&resp)
	return resp
}

func extractJSONBuiltins(vm *goja.Runtime) (jsonParse, jsonStringify goja.Callable, err error) {
	jsonObj := vm.Get("JSON").ToObject(vm)
	parseFn, ok := goja.AssertFunction(jsonObj.Get("parse"))
	if !ok {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: "JSON.parse builtin unavailable",
		}
	}
	stringifyFn, ok := goja.AssertFunction(jsonObj.Get("stringify"))
	if !ok {
		return nil, nil, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: "JSON.stringify builtin unavailable",
		}
	}
	return parseFn, stringifyFn, nil
}

func installSandboxHygiene(vm *goja.Runtime) error {
	for _, globalName := range []string{"process", "Buffer", "__dirname", "__filename", "module", "exports"} {
		_ = vm.GlobalObject().Delete(globalName)
	}

	throwUnsupported := func(msg string) func(goja.FunctionCall) goja.Value {
		return func(goja.FunctionCall) goja.Value {
			throwJSError(vm, msg)
			return goja.Undefined()
		}
	}

	unsupportedFuncs := map[string]string{
		"setTimeout":     "setTimeout is not supported in knurl; tools must be synchronous ES6 JavaScript",
		"setInterval":    "setInterval is not supported in knurl; tools must be synchronous ES6 JavaScript",
		"setImmediate":   "setImmediate is not supported in knurl; tools must be synchronous ES6 JavaScript",
		"require":        "require is not supported in knurl; tools must be single-file synchronous ES6 JavaScript",
		"fetch":          "fetch is not supported in knurl; use knurl.http",
		"XMLHttpRequest": "XMLHttpRequest is not supported in knurl; use knurl.http",
	}
	for name, msg := range unsupportedFuncs {
		if err := vm.Set(name, throwUnsupported(msg)); err != nil {
			return err
		}
	}

	consoleObj := vm.NewObject()
	for _, method := range []string{"log", "info", "warn", "error", "debug"} {
		if err := consoleObj.Set(method, throwUnsupported("console is not supported in knurl; use knurl.log(message)")); err != nil {
			return err
		}
	}
	if err := vm.Set("console", consoleObj); err != nil {
		return err
	}

	promiseFn := vm.ToValue(throwUnsupported("Promise is not supported in knurl; tools must be synchronous ES6 JavaScript"))
	promiseObj := promiseFn.ToObject(vm)
	for _, method := range []string{"resolve", "reject", "all", "race", "allSettled", "any"} {
		if err := promiseObj.Set(method, throwUnsupported("Promise is not supported in knurl; tools must be synchronous ES6 JavaScript")); err != nil {
			return err
		}
	}
	return vm.Set("Promise", promiseObj)
}

func bindKnurlHost(ctx context.Context, vm *goja.Runtime, h *host.Host, jsonParse, jsonStringify goja.Callable) error {
	knurlObj := vm.NewObject()

	fsObj := vm.NewObject()
	if err := fsObj.Set("read", func(call goja.FunctionCall) goja.Value {
		pathArg := requireStringArg(vm, call, "knurl.fs.read requires a path string")
		opts, err := parseFSReadOpts(call.Argument(1))
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		content, readErr := h.FSRead(pathArg, opts)
		if readErr != nil {
			throwHostError(vm, jsonParse, readErr)
		}
		return vm.ToValue(content)
	}); err != nil {
		return err
	}
	if err := fsObj.Set("glob", func(call goja.FunctionCall) goja.Value {
		patArg := requireStringArg(vm, call, "knurl.fs.glob requires a pattern string")
		opts, err := parseFSGlobOpts(call.Argument(1))
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		files, globErr := h.FSGlob(patArg, opts)
		if globErr != nil {
			throwHostError(vm, jsonParse, globErr)
		}
		return mustToNativeJS(vm, jsonParse, files)
	}); err != nil {
		return err
	}
	if err := fsObj.Set("stat", func(call goja.FunctionCall) goja.Value {
		pathArg := requireStringArg(vm, call, "knurl.fs.stat requires a path string")
		st, statErr := h.FSStat(pathArg)
		if statErr != nil {
			throwHostError(vm, jsonParse, statErr)
		}
		return mustToNativeJS(vm, jsonParse, st)
	}); err != nil {
		return err
	}
	if err := knurlObj.Set("fs", fsObj); err != nil {
		return err
	}

	gitObj := vm.NewObject()
	if err := gitObj.Set("branch", func(goja.FunctionCall) goja.Value {
		br, err := h.GitBranch(ctx)
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		return mustToNativeJS(vm, jsonParse, map[string]any{
			"current":  br.Current,
			"detached": br.Detached,
			"head":     br.Head,
			"upstream": br.Upstream,
		})
	}); err != nil {
		return err
	}
	if err := gitObj.Set("status", func(goja.FunctionCall) goja.Value {
		st, err := h.GitStatus(ctx)
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		return mustToNativeJS(vm, jsonParse, st)
	}); err != nil {
		return err
	}
	if err := gitObj.Set("log", func(call goja.FunctionCall) goja.Value {
		opts, err := parseGitLogOpts(call.Argument(0))
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		commits, logErr := h.GitLog(ctx, opts)
		if logErr != nil {
			throwHostError(vm, jsonParse, logErr)
		}
		return mustToNativeJS(vm, jsonParse, commits)
	}); err != nil {
		return err
	}
	if err := gitObj.Set("diff", func(call goja.FunctionCall) goja.Value {
		opts, err := parseGitDiffOpts(call.Argument(0))
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		diff, diffErr := h.GitDiff(ctx, opts)
		if diffErr != nil {
			throwHostError(vm, jsonParse, diffErr)
		}
		return mustToNativeJS(vm, jsonParse, diff)
	}); err != nil {
		return err
	}
	if err := knurlObj.Set("git", gitObj); err != nil {
		return err
	}

	httpObj := vm.NewObject()
	if err := httpObj.Set("get", func(call goja.FunctionCall) goja.Value {
		urlStr := requireStringArg(vm, call, "knurl.http.get requires a URL string")
		opts, err := parseHTTPOpts(call.Argument(1))
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		res, httpErr := h.HTTPGet(ctx, urlStr, opts)
		if httpErr != nil {
			throwHostError(vm, jsonParse, httpErr)
		}
		return mustToNativeJS(vm, jsonParse, res)
	}); err != nil {
		return err
	}
	if err := httpObj.Set("post", func(call goja.FunctionCall) goja.Value {
		urlStr := requireStringArg(vm, call, "knurl.http.post requires a URL string")
		bodyBytes, isJSON, bodyErr := parseHTTPBody(jsonStringify, call.Argument(1))
		if bodyErr != nil {
			throwHostError(vm, jsonParse, bodyErr)
		}
		opts, err := parseHTTPOpts(call.Argument(2))
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		res, httpErr := h.HTTPPost(ctx, urlStr, bodyBytes, isJSON, opts)
		if httpErr != nil {
			throwHostError(vm, jsonParse, httpErr)
		}
		return mustToNativeJS(vm, jsonParse, res)
	}); err != nil {
		return err
	}
	if err := knurlObj.Set("http", httpObj); err != nil {
		return err
	}

	if err := knurlObj.Set("exec", func(call goja.FunctionCall) goja.Value {
		tmplID := requireStringArg(vm, call, "knurl.exec requires a templateId string")
		params, err := parseExecParams(call.Argument(1))
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		opts, err := parseExecOpts(call.Argument(2))
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		res, execErr := h.Exec(ctx, tmplID, params, opts)
		if execErr != nil {
			throwHostError(vm, jsonParse, execErr)
		}
		return mustToNativeJS(vm, jsonParse, res)
	}); err != nil {
		return err
	}

	secretsObj := vm.NewObject()
	if err := secretsObj.Set("has", func(call goja.FunctionCall) goja.Value {
		name := requireStringArg(vm, call, "knurl.secrets.has requires a secret name string")
		has, err := h.SecretsHas(name)
		if err != nil {
			throwHostError(vm, jsonParse, err)
		}
		return vm.ToValue(has)
	}); err != nil {
		return err
	}
	if err := knurlObj.Set("secrets", secretsObj); err != nil {
		return err
	}

	if err := knurlObj.Set("log", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 && !goja.IsUndefined(call.Argument(0)) && !goja.IsNull(call.Argument(0)) {
			h.Log(call.Argument(0).String())
		}
		return goja.Undefined()
	}); err != nil {
		return err
	}

	return vm.Set("knurl", knurlObj)
}

func requireStringArg(vm *goja.Runtime, call goja.FunctionCall, msg string) string {
	arg := call.Argument(0)
	if goja.IsUndefined(arg) || goja.IsNull(arg) {
		throwHostError(vm, nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: msg,
		})
	}
	if _, ok := arg.Export().(string); !ok {
		throwHostError(vm, nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: msg,
		})
	}
	return arg.String()
}

func parseOptionsMap(arg goja.Value) (map[string]any, error) {
	if arg == nil || goja.IsUndefined(arg) || goja.IsNull(arg) {
		return nil, nil
	}
	exported := arg.Export()
	m, ok := exported.(map[string]any)
	if !ok {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "options argument must be an object",
		}
	}
	return m, nil
}

func extractIntOpt(m map[string]any, key string) (int, bool, error) {
	val, exists := m[key]
	if !exists || val == nil {
		return 0, false, nil
	}
	switch n := val.(type) {
	case int64:
		return int(n), true, nil
	case int:
		return n, true, nil
	case float64:
		if n != float64(int64(n)) {
			return 0, false, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("option %q must be an integer", key),
			}
		}
		return int(n), true, nil
	default:
		return 0, false, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("option %q must be an integer", key),
		}
	}
}

func extractBoolOpt(m map[string]any, key string) (bool, error) {
	val, exists := m[key]
	if !exists || val == nil {
		return false, nil
	}
	b, ok := val.(bool)
	if !ok {
		return false, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("option %q must be a boolean", key),
		}
	}
	return b, nil
}

func extractStringOpt(m map[string]any, key string) (string, error) {
	val, exists := m[key]
	if !exists || val == nil {
		return "", nil
	}
	s, ok := val.(string)
	if !ok {
		return "", &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("option %q must be a string", key),
		}
	}
	return s, nil
}

func parseFSReadOpts(arg goja.Value) (host.FSReadOptions, error) {
	m, err := parseOptionsMap(arg)
	if err != nil || m == nil {
		return host.FSReadOptions{}, err
	}
	maxBytes, hasMax, err := extractIntOpt(m, "max_bytes")
	if err != nil {
		return host.FSReadOptions{}, err
	}
	truncate, err := extractBoolOpt(m, "truncate")
	if err != nil {
		return host.FSReadOptions{}, err
	}
	return host.FSReadOptions{MaxBytes: maxBytes, HasMax: hasMax, Truncate: truncate}, nil
}

func parseFSGlobOpts(arg goja.Value) (host.FSGlobOptions, error) {
	m, err := parseOptionsMap(arg)
	if err != nil || m == nil {
		return host.FSGlobOptions{}, err
	}
	maxResults, hasMax, err := extractIntOpt(m, "max_results")
	if err != nil {
		return host.FSGlobOptions{}, err
	}
	includeHidden, err := extractBoolOpt(m, "include_hidden")
	if err != nil {
		return host.FSGlobOptions{}, err
	}
	var ignore []string
	if rawIgnore, ok := m["ignore"]; ok && rawIgnore != nil {
		items, isSlice := rawIgnore.([]any)
		if !isSlice {
			return host.FSGlobOptions{}, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: `option "ignore" must be an array of strings`,
			}
		}
		ignore = make([]string, len(items))
		for i, item := range items {
			s, isStr := item.(string)
			if !isStr {
				return host.FSGlobOptions{}, &contract.ToolError{
					Code:    contract.ErrInvalidArgument,
					Message: `option "ignore" elements must be strings`,
				}
			}
			ignore[i] = s
		}
	}
	return host.FSGlobOptions{
		MaxResults:    maxResults,
		HasMax:        hasMax,
		IncludeHidden: includeHidden,
		Ignore:        ignore,
	}, nil
}

func parseGitLogOpts(arg goja.Value) (host.GitLogOptions, error) {
	m, err := parseOptionsMap(arg)
	if err != nil || m == nil {
		return host.GitLogOptions{}, err
	}
	limit, hasLimit, err := extractIntOpt(m, "limit")
	if err != nil {
		return host.GitLogOptions{}, err
	}
	ref, err := extractStringOpt(m, "ref")
	if err != nil {
		return host.GitLogOptions{}, err
	}
	pathStr, err := extractStringOpt(m, "path")
	if err != nil {
		return host.GitLogOptions{}, err
	}
	return host.GitLogOptions{Limit: limit, HasLimit: hasLimit, Ref: ref, Path: pathStr}, nil
}

func parseGitDiffOpts(arg goja.Value) (host.GitDiffOptions, error) {
	m, err := parseOptionsMap(arg)
	if err != nil || m == nil {
		return host.GitDiffOptions{}, err
	}
	base, err := extractStringOpt(m, "base")
	if err != nil {
		return host.GitDiffOptions{}, err
	}
	head, err := extractStringOpt(m, "head")
	if err != nil {
		return host.GitDiffOptions{}, err
	}
	staged, err := extractBoolOpt(m, "staged")
	if err != nil {
		return host.GitDiffOptions{}, err
	}
	statOnly, err := extractBoolOpt(m, "stat_only")
	if err != nil {
		return host.GitDiffOptions{}, err
	}
	pathStr, err := extractStringOpt(m, "path")
	if err != nil {
		return host.GitDiffOptions{}, err
	}
	return host.GitDiffOptions{
		Base:     base,
		Head:     head,
		Staged:   staged,
		StatOnly: statOnly,
		Path:     pathStr,
	}, nil
}

func parseExecParams(arg goja.Value) (map[string]string, error) {
	m, err := parseOptionsMap(arg)
	if err != nil || m == nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: fmt.Sprintf("exec param %q must be a string", k),
			}
		}
		out[k] = s
	}
	return out, nil
}

func parseExecOpts(arg goja.Value) (host.ExecOptions, error) {
	m, err := parseOptionsMap(arg)
	if err != nil || m == nil {
		return host.ExecOptions{}, err
	}
	timeoutMs, hasTimeout, err := extractIntOpt(m, "timeout_ms")
	if err != nil {
		return host.ExecOptions{}, err
	}
	return host.ExecOptions{TimeoutMs: timeoutMs, HasTimeout: hasTimeout}, nil
}

func parseHTTPOpts(arg goja.Value) (host.HTTPOptions, error) {
	m, err := parseOptionsMap(arg)
	if err != nil || m == nil {
		return host.HTTPOptions{}, err
	}
	timeoutMs, hasTimeout, err := extractIntOpt(m, "timeout_ms")
	if err != nil {
		return host.HTTPOptions{}, err
	}
	var headers map[string]string
	if rawHeaders, ok := m["headers"]; ok && rawHeaders != nil {
		hdrMap, isMap := rawHeaders.(map[string]any)
		if !isMap {
			return host.HTTPOptions{}, &contract.ToolError{
				Code:    contract.ErrInvalidArgument,
				Message: `option "headers" must be an object of strings`,
			}
		}
		headers = make(map[string]string, len(hdrMap))
		for k, v := range hdrMap {
			s, isStr := v.(string)
			if !isStr {
				return host.HTTPOptions{}, &contract.ToolError{
					Code:    contract.ErrInvalidArgument,
					Message: fmt.Sprintf("HTTP header %q value must be a string", k),
				}
			}
			headers[k] = s
		}
	}
	return host.HTTPOptions{
		Headers:    headers,
		TimeoutMs:  timeoutMs,
		HasTimeout: hasTimeout,
	}, nil
}

func parseHTTPBody(jsonStringify goja.Callable, arg goja.Value) ([]byte, bool, error) {
	if arg == nil || goja.IsUndefined(arg) || goja.IsNull(arg) {
		return nil, false, nil
	}
	if s, ok := arg.Export().(string); ok {
		return []byte(s), false, nil
	}
	strVal, err := jsonStringify(goja.Undefined(), arg)
	if err != nil {
		return nil, false, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: fmt.Sprintf("HTTP POST body is not JSON-serializable: %v", err),
		}
	}
	if strVal == nil || goja.IsUndefined(strVal) {
		return nil, false, &contract.ToolError{
			Code:    contract.ErrInvalidArgument,
			Message: "HTTP POST body is not JSON-serializable",
		}
	}
	return []byte(strVal.String()), true, nil
}

func toNativeJS(vm *goja.Runtime, jsonParse goja.Callable, v any) (goja.Value, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("marshal host value: %v", err),
		}
	}
	val, parseErr := jsonParse(goja.Undefined(), vm.ToValue(string(raw)))
	if parseErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInternalError,
			Message: fmt.Sprintf("parse host JSON value in VM: %v", parseErr),
		}
	}
	return val, nil
}

func mustToNativeJS(vm *goja.Runtime, jsonParse goja.Callable, v any) goja.Value {
	val, err := toNativeJS(vm, jsonParse, v)
	if err != nil {
		throwHostError(vm, jsonParse, err)
	}
	return val
}

func throwJSError(vm *goja.Runtime, msg string) {
	if errCtor, ok := goja.AssertConstructor(vm.Get("Error")); ok {
		if errObj, err := errCtor(nil, vm.ToValue(msg)); err == nil {
			panic(errObj)
		}
	}
	panic(vm.ToValue(msg))
}

func throwHostError(vm *goja.Runtime, jsonParse goja.Callable, err error) {
	toolErr := asToolError(err)
	if errCtor, ok := goja.AssertConstructor(vm.Get("Error")); ok {
		if errObj, ctorErr := errCtor(nil, vm.ToValue(toolErr.Message)); ctorErr == nil {
			_ = errObj.Set("code", string(toolErr.Code))
			if toolErr.Details != nil && jsonParse != nil {
				if detVal, detErr := toNativeJS(vm, jsonParse, toolErr.Details); detErr == nil {
					_ = errObj.Set("details", detVal)
				}
			}
			panic(errObj)
		}
	}
	panic(vm.ToValue(toolErr.Error()))
}

func classifyVMError(ctx context.Context, err error) *contract.ToolError {
	if ctx.Err() != nil {
		return &contract.ToolError{
			Code:    contract.ErrTimeout,
			Message: "tool execution exceeded timeout",
		}
	}
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return &contract.ToolError{
			Code:    contract.ErrTimeout,
			Message: "tool execution interrupted due to timeout",
		}
	}
	var exc *goja.Exception
	if errors.As(err, &exc) {
		if hostErr := extractHostErrorFromJSValue(exc.Value()); hostErr != nil {
			return hostErr
		}
		return &contract.ToolError{
			Code:    contract.ErrToolError,
			Message: exc.String(),
		}
	}
	return &contract.ToolError{
		Code:    contract.ErrToolError,
		Message: err.Error(),
	}
}

func extractHostErrorFromJSValue(val goja.Value) *contract.ToolError {
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil
	}
	exported := val.Export()
	m, ok := exported.(map[string]any)
	if !ok {
		return nil
	}
	rawCode, ok := m["code"].(string)
	if !ok {
		return nil
	}
	code := contract.ErrorCode(rawCode)
	if _, known := knownHostErrorCodes[code]; !known {
		return nil
	}
	msg := string(code)
	if rawMsg, ok := m["message"].(string); ok && rawMsg != "" {
		msg = rawMsg
	}
	var details map[string]any
	if rawDetails, ok := m["details"].(map[string]any); ok {
		details = rawDetails
	}
	return &contract.ToolError{
		Code:    code,
		Message: msg,
		Details: details,
	}
}

func extractJSOutputObject(jsonStringify goja.Callable, resVal goja.Value) (map[string]any, *contract.ToolError) {
	if resVal == nil || goja.IsUndefined(resVal) || goja.IsNull(resVal) {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidOutput,
			Message: "main(input) must return a JSON object, got null or undefined",
		}
	}
	strVal, err := jsonStringify(goja.Undefined(), resVal)
	if err != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidOutput,
			Message: fmt.Sprintf("main(input) return value is not JSON-serializable: %v", err),
		}
	}
	if strVal == nil || goja.IsUndefined(strVal) {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidOutput,
			Message: "main(input) must return a JSON object",
		}
	}
	raw := strings.TrimSpace(strVal.String())
	if !strings.HasPrefix(raw, "{") {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidOutput,
			Message: "main(input) must return a plain JSON object",
		}
	}
	if len(raw) > MaxIPCFrameBytes {
		return nil, &contract.ToolError{
			Code:    contract.ErrLimitExceeded,
			Message: fmt.Sprintf("tool output size %d exceeds %d byte IPC cap", len(raw), MaxIPCFrameBytes),
		}
	}
	var out map[string]any
	if unmarshalErr := json.Unmarshal([]byte(raw), &out); unmarshalErr != nil {
		return nil, &contract.ToolError{
			Code:    contract.ErrInvalidOutput,
			Message: fmt.Sprintf("decode tool output JSON: %v", unmarshalErr),
		}
	}
	return out, nil
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
