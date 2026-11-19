package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

// Config configures the Phase 1 MCP server.
type Config struct {
	Loader     *registry.Loader
	Supervisor *runtime.Supervisor
	WorkDir    string
	Version    string
	Logger     *slog.Logger
}

// NewServer constructs a stateless MCP server exposing only knurl_run in Phase 1.
func NewServer(cfg Config) *mcp.Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	loader := cfg.Loader
	if loader == nil {
		loader = &registry.Loader{}
	}
	supervisor := cfg.Supervisor
	if supervisor == nil {
		supervisor = &runtime.Supervisor{}
	}
	version := cfg.Version
	if version == "" {
		version = "0.0.0-dev"
	}

	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "knurl",
			Version: version,
		},
		&mcp.ServerOptions{
			Logger: logger,
			Capabilities: &mcp.ServerCapabilities{
				Tools: &mcp.ToolCapabilities{},
			},
		},
	)

	mcp.AddTool(server, &mcp.Tool{
		Name:         "knurl_run",
		Description:  "Validate typed JSON input against a tool manifest, execute tool.js in an isolated _worker subprocess, redact secrets, and validate structured JSON output.",
		InputSchema:  runToolInputSchema(),
		OutputSchema: runToolOutputSchema(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, req contract.RunRequest) (*mcp.CallToolResult, contract.RunResponse, error) {
		start := time.Now()
		scope := req.Scope
		if scope == "" {
			scope = contract.ScopeAuto
		}
		failResp := func(version int, resScope contract.Scope, err error) (*mcp.CallToolResult, contract.RunResponse, error) {
			return &mcp.CallToolResult{IsError: true}, contract.RunResponse{
				OK:         false,
				Tool:       req.Name,
				Version:    version,
				Scope:      resScope,
				DurationMs: time.Since(start).Milliseconds(),
				Error:      asToolError(err),
			}, nil
		}

		if scope == contract.ScopeGlobal {
			return failResp(0, contract.ScopeGlobal, &contract.ToolError{
				Code:    contract.ErrToolNotFound,
				Message: fmt.Sprintf("tool %q not found in global scope", req.Name),
			})
		}

		bundle, loadErr := loader.LoadByName(req.Name)
		if loadErr != nil {
			return failResp(0, scope, loadErr)
		}

		if req.Version > 0 && req.Version != bundle.Version {
			return failResp(req.Version, bundle.Scope, &contract.ToolError{
				Code:    contract.ErrVersionNotFound,
				Message: fmt.Sprintf("tool %q version v%d not found (active: v%d)", req.Name, req.Version, bundle.Version),
			})
		}

		res := supervisor.ExecuteBundle(
			ctx,
			bundle.Manifest,
			bundle.Source,
			cfg.WorkDir,
			req.Input,
			req.TimeoutMs,
			bundle.Scope,
		)
		return &mcp.CallToolResult{IsError: !res.OK}, res, nil
	})

	return server
}

func runToolInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []string{"name", "input"},
		"additionalProperties": false,
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Approved tool identifier to execute.",
			},
			"input": map[string]any{
				"type":        "object",
				"description": "Typed JSON input validated against the tool's input_schema.",
			},
			"scope": map[string]any{
				"type":        "string",
				"enum":        []string{"auto", "repo", "global"},
				"default":     "auto",
				"description": "Registry scope to resolve the tool from.",
			},
			"version": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Optional explicit tool version number.",
			},
			"timeout_ms": map[string]any{
				"type":        "integer",
				"minimum":     100,
				"maximum":     120000,
				"description": "Optional execution timeout override in milliseconds.",
			},
		},
	}
}

func runToolOutputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"required":             []string{"ok", "tool", "version", "scope", "duration_ms"},
		"additionalProperties": false,
		"properties": map[string]any{
			"ok":          map[string]any{"type": "boolean"},
			"tool":        map[string]any{"type": "string"},
			"version":     map[string]any{"type": "integer"},
			"scope":       map[string]any{"type": "string"},
			"duration_ms": map[string]any{"type": "integer"},
			"output":      map[string]any{"type": "object"},
			"error": map[string]any{
				"type":     "object",
				"required": []string{"code", "message"},
				"properties": map[string]any{
					"code":    map[string]any{"type": "string"},
					"message": map[string]any{"type": "string"},
					"details": map[string]any{"type": "object"},
				},
			},
			"diagnostics": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
	}
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
