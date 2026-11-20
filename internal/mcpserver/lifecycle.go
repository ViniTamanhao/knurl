package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/ViniTamanhao/knurl/internal/approval"
	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/ViniTamanhao/knurl/internal/runtime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func annotations(readOnly, destructive, idempotent, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, IdempotentHint: idempotent, OpenWorldHint: &openWorld}
}
func objectSchema(required []string, props map[string]any) map[string]any {
	out := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func stringSchema() map[string]any { return map[string]any{"type": "string"} }
func scopeSchema(values ...string) map[string]any {
	schema := map[string]any{"type": "string", "enum": values}
	if len(values) > 0 && (values[0] == "auto" || values[0] == "all") {
		schema["default"] = values[0]
	}
	return schema
}
func lifecycleResult(value any, err error) (*mcp.CallToolResult, map[string]any, error) {
	out := map[string]any{}
	if err != nil {
		out["error"] = asToolError(err)
		return &mcp.CallToolResult{IsError: true}, out, nil
	}
	data, e := json.Marshal(value)
	if e != nil {
		return nil, nil, e
	}
	if e = json.Unmarshal(data, &out); e != nil {
		return nil, nil, e
	}
	return nil, out, nil
}

// Preserve raw bundle JSON until the frozen schemas reject unknown fields.
type proposalInput struct {
	Name        string          `json:"name"`
	Scope       contract.Scope  `json:"scope"`
	Summary     string          `json:"summary"`
	BaseVersion *int            `json:"base_version,omitempty"`
	Manifest    json.RawMessage `json:"manifest"`
	Source      string          `json:"source"`
	Tests       json.RawMessage `json:"tests"`
}

func addLifecycleTools(server *mcp.Server, reg *registry.Registry, sup *runtime.Supervisor, workDir string) {
	store := &approval.Store{Registry: reg, Supervisor: sup, WorkDir: workDir}
	mcp.AddTool(server, &mcp.Tool{Name: "knurl_discover", Description: "Search approved tools by keyword, tags, and scope; returns compact metadata only.", Annotations: annotations(true, false, true, false), InputSchema: objectSchema(nil, map[string]any{"query": stringSchema(), "tags": map[string]any{"type": "array", "items": stringSchema()}, "scope": scopeSchema("all", "repo", "global"), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 25}})}, func(_ context.Context, _ *mcp.CallToolRequest, req contract.DiscoverRequest) (*mcp.CallToolResult, map[string]any, error) {
		out, err := reg.Discover(req)
		return lifecycleResult(out, err)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "knurl_inspect", Description: "Read an approved tool manifest, schemas, capability declarations and version history.", Annotations: annotations(true, false, true, false), InputSchema: objectSchema([]string{"name"}, map[string]any{"name": stringSchema(), "scope": scopeSchema("auto", "repo", "global"), "version": map[string]any{"type": "integer", "minimum": 1}})}, func(_ context.Context, _ *mcp.CallToolRequest, req contract.InspectRequest) (*mcp.CallToolResult, map[string]any, error) {
		out, err := reg.Inspect(req)
		return lifecycleResult(out, err)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "knurl_propose", Description: "Validate and stage a new tool or repair. Never approves or modifies active tools. Elevated proposals use fixture mocks only.", Annotations: annotations(false, false, false, false), InputSchema: objectSchema([]string{"name", "scope", "summary", "manifest", "source", "tests"}, map[string]any{"name": stringSchema(), "scope": scopeSchema("repo", "global"), "summary": map[string]any{"type": "string", "minLength": 5, "maxLength": 200}, "manifest": map[string]any{"type": "object"}, "source": map[string]any{"type": "string", "minLength": 1, "maxLength": 1048576}, "tests": map[string]any{"type": "object"}, "base_version": map[string]any{"type": "integer", "minimum": 0}})}, func(ctx context.Context, _ *mcp.CallToolRequest, req proposalInput) (*mcp.CallToolResult, map[string]any, error) {
		manifest, err := contract.ValidateManifestJSON(req.Manifest)
		if err != nil {
			return lifecycleResult(nil, err)
		}
		tests, err := contract.ValidateTestsJSON(req.Tests, manifest)
		if err != nil {
			return lifecycleResult(nil, err)
		}
		out, err := store.Propose(ctx, contract.ProposeRequest{Name: req.Name, Scope: req.Scope, Summary: req.Summary, BaseVersion: req.BaseVersion, Manifest: manifest, Source: req.Source, Tests: tests})
		return lifecycleResult(out, err)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "knurl_status", Description: "Read a proposal state, human decision and promoted version.", Annotations: annotations(true, false, true, false), InputSchema: objectSchema([]string{"proposal_id"}, map[string]any{"proposal_id": map[string]any{"type": "string", "minLength": 1}})}, func(_ context.Context, _ *mcp.CallToolRequest, req contract.StatusRequest) (*mcp.CallToolResult, map[string]any, error) {
		out, err := store.Status(req.ProposalID)
		return lifecycleResult(out, err)
	})
}
