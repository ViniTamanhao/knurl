package host

import (
	"fmt"
	"maps"
	"strings"

	"github.com/ViniTamanhao/knurl/internal/contract"
)

func mockMissing(operation string) error {
	return &contract.ToolError{Code: contract.ErrCapabilityDenied, Message: "fixture has no mock for " + operation}
}

func (h *Host) mockExec(id string, params map[string]string) (ExecResult, error) {
	for _, m := range h.mocks.Exec {
		if m.TemplateID != id || !maps.Equal(m.Params, params) {
			continue
		}
		if m.Error != "" {
			return ExecResult{}, &contract.ToolError{Code: m.Error, Message: "mock exec error"}
		}
		if m.Result == nil {
			break
		}
		r := m.Result
		if len(r.Stdout) > MaxExecStreamBytes || len(r.Stderr) > MaxExecStreamBytes {
			return ExecResult{}, &contract.ToolError{Code: contract.ErrLimitExceeded, Message: "mock exec stream exceeds cap"}
		}
		return ExecResult{ExitCode: r.ExitCode, Stdout: h.redactor.String(r.Stdout), Stderr: h.redactor.String(r.Stderr)}, nil
	}
	return ExecResult{}, mockMissing("exec " + id)
}

func (h *Host) mockHTTP(method, url string, headers map[string]string) (HTTPResponse, error) {
	for _, m := range h.mocks.HTTP {
		if m.Method != method || m.URL != url {
			continue
		}
		for k, v := range m.RequireHeaders {
			for name, value := range h.secrets {
				v = strings.ReplaceAll(v, "${secret:"+name+"}", value)
			}
			found := false
			for hk, hv := range headers {
				if strings.EqualFold(k, hk) && v == hv {
					found = true
				}
			}
			if !found {
				return HTTPResponse{}, &contract.ToolError{Code: contract.ErrTestFailed, Message: fmt.Sprintf("mock header %s mismatch", k)}
			}
		}
		if m.Error != "" {
			return HTTPResponse{}, &contract.ToolError{Code: m.Error, Message: "mock HTTP error"}
		}
		if m.Result == nil {
			break
		}
		r := m.Result
		if len(r.Body) > MaxHTTPBodyBytes {
			return HTTPResponse{}, &contract.ToolError{Code: contract.ErrLimitExceeded, Message: "mock HTTP body exceeds cap"}
		}
		return HTTPResponse{Status: r.Status, Headers: r.Headers, Body: h.redactor.String(r.Body)}, nil
	}
	return HTTPResponse{}, mockMissing("HTTP " + method + " " + url)
}
