package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/mcpserver"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_worker" {
		os.Exit(runtime.RunWorker(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func decodeCallToolEnvelope(t *testing.T, res *mcp.CallToolResult) contract.RunResponse {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatalf("expected non-nil StructuredContent")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	var out contract.RunResponse
	if unmarshalErr := json.Unmarshal(raw, &out); unmarshalErr != nil {
		t.Fatalf("unmarshal RunResponse: %v (raw=%s)", unmarshalErr, raw)
	}
	return out
}

func writeVersionedMCPTool(t *testing.T, toolsDir, name string, ver int, tierLabel string) {
	t.Helper()
	verDir := filepath.Join(toolsDir, name, fmt.Sprintf("v%d", ver))
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", verDir, err)
	}
	manifest := fmt.Sprintf(`{
  "schema_version": "1",
  "name": %q,
  "version": %d,
  "description": "Versioned MCP test tool",
  "read_only": true,
  "input_schema": {"type": "object", "additionalProperties": false},
  "output_schema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["tier", "ver"],
    "properties": {
      "tier": {"type": "string"},
      "ver": {"type": "integer"}
    }
  },
  "capabilities": {}
}`, name, ver)
	if err := os.WriteFile(filepath.Join(verDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	src := fmt.Sprintf(`function main() { return { tier: %q, ver: %d }; }`, tierLabel, ver)
	if err := os.WriteFile(filepath.Join(verDir, "tool.js"), []byte(src), 0o600); err != nil {
		t.Fatalf("write tool.js: %v", err)
	}
}

func TestMCPServerKnurlRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workDir := t.TempDir()
	runGit(t, workDir, "init", "-b", "main")
	runGit(t, workDir, "config", "user.name", "Test")
	runGit(t, workDir, "config", "user.email", "test@example.com")
	runGit(t, workDir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Demo\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "go.mod"), []byte("module example.com/demo\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	runGit(t, workDir, "add", "README.md", "go.mod")
	runGit(t, workDir, "commit", "-m", "init repo")

	examplesDir := filepath.Join("..", "..", "examples")
	t.Setenv("VPS_SSH_KEY_PATH", "")

	server := mcpserver.NewServer(mcpserver.Config{
		Registry: &registry.Registry{WorkDir: workDir, OverrideDir: examplesDir},
		WorkDir:  workDir,
		Version:  "0.1.0-test",
	})
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.Run(ctx, serverTransport)
	}()

	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer func() {
		_ = session.Close()
		cancel()
		if srvErr := <-serverErrCh; srvErr != nil && !errors.Is(srvErr, context.Canceled) {
			t.Errorf("server.Run returned unexpected error: %v", srvErr)
		}
	}()

	toolsRes, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(toolsRes.Tools) != 5 {
		t.Fatalf("ListTools = %+v, want five static tools", toolsRes.Tools)
	}

	// 1. Happy path: repo-context over MCP
	callRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "knurl_run",
		Arguments: map[string]any{
			"name":  "repo-context",
			"input": map[string]any{"recent_commits": 5},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(repo-context): %v", err)
	}
	if callRes.IsError {
		t.Fatalf("expected IsError=false, got content=%+v", callRes.Content)
	}
	env := decodeCallToolEnvelope(t, callRes)
	if !env.OK || env.Tool != "repo-context" || env.Version != 1 || env.Scope != contract.ScopeOverride {
		t.Fatalf("unexpected RunResponse: %+v", env)
	}
	if isGit, _ := env.Output["is_git_repo"].(bool); !isGit {
		t.Fatalf("expected is_git_repo=true, got %+v", env.Output)
	}

	// 2. INVALID_INPUT propagated with IsError=true
	badInputRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "knurl_run",
		Arguments: map[string]any{
			"name":  "repo-context",
			"input": map[string]any{"recent_commits": 500},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(invalid input): %v", err)
	}
	if !badInputRes.IsError {
		t.Fatalf("expected IsError=true for INVALID_INPUT")
	}
	badEnv := decodeCallToolEnvelope(t, badInputRes)
	if badEnv.OK || badEnv.Error == nil || badEnv.Error.Code != contract.ErrInvalidInput {
		t.Fatalf("expected INVALID_INPUT envelope, got %+v", badEnv)
	}

	// 3. TOOL_NOT_FOUND (including path traversal name)
	notFoundRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "knurl_run",
		Arguments: map[string]any{
			"name":  "../repo-context",
			"input": map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(path traversal): %v", err)
	}
	if !notFoundRes.IsError {
		t.Fatalf("expected IsError=true for traversal name")
	}
	nfEnv := decodeCallToolEnvelope(t, notFoundRes)
	if nfEnv.Error == nil || nfEnv.Error.Code != contract.ErrToolNotFound {
		t.Fatalf("expected TOOL_NOT_FOUND, got %+v", nfEnv)
	}

	// 4. VERSION_NOT_FOUND
	badVerRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "knurl_run",
		Arguments: map[string]any{
			"name":    "repo-context",
			"version": 99,
			"input":   map[string]any{},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(bad version): %v", err)
	}
	verEnv := decodeCallToolEnvelope(t, badVerRes)
	if verEnv.Error == nil || verEnv.Error.Code != contract.ErrVersionNotFound {
		t.Fatalf("expected VERSION_NOT_FOUND, got %+v", verEnv)
	}

	// 5. SECRET_MISSING for vps-health when VPS_SSH_KEY_PATH is unset
	secRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "knurl_run",
		Arguments: map[string]any{
			"name":  "vps-health",
			"input": map[string]any{"host": "prod.example.com", "user": "ops"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool(vps-health missing secret): %v", err)
	}
	secEnv := decodeCallToolEnvelope(t, secRes)
	if secEnv.Error == nil || secEnv.Error.Code != contract.ErrSecretMissing {
		t.Fatalf("expected SECRET_MISSING, got %+v", secEnv)
	}

	// 6. MCP-level input schema validation rejects unknown properties
	mcpInvalidRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "knurl_run",
		Arguments: map[string]any{
			"name":       "repo-context",
			"input":      map[string]any{},
			"unexpected": true,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(extra field) returned protocol error: %v", err)
	}
	if !mcpInvalidRes.IsError {
		t.Fatalf("expected IsError=true when knurl_run receives unknown property")
	}
}

func TestMCPServerTwoTierRegistryResolution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	workDir := filepath.Join(repoRoot, "nested")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}

	repoTools := filepath.Join(repoRoot, ".knurl", "tools")
	globalHome := filepath.Join(root, "global-home")
	globalTools := filepath.Join(globalHome, "tools")

	// Shadowed tool: repo has v1 & v2 (active=2); global has v1.
	writeVersionedMCPTool(t, repoTools, "dual-tool", 1, "repo")
	writeVersionedMCPTool(t, repoTools, "dual-tool", 2, "repo")
	if err := os.WriteFile(filepath.Join(repoTools, "dual-tool", "active.json"), []byte(`{"active_version":2,"previous_version":1,"updated_at":"2026-11-19T12:00:00Z"}`), 0o600); err != nil {
		t.Fatalf("write active.json: %v", err)
	}
	writeVersionedMCPTool(t, globalTools, "dual-tool", 1, "global")
	writeVersionedMCPTool(t, globalTools, "global-only", 1, "global")

	t.Setenv("KNURL_HOME", globalHome)

	server := mcpserver.NewServer(mcpserver.Config{
		WorkDir: workDir,
		Version: "0.1.0-test",
	})
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- server.Run(ctx, serverTransport)
	}()
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer func() {
		_ = session.Close()
		cancel()
		if srvErr := <-serverErrCh; srvErr != nil && !errors.Is(srvErr, context.Canceled) {
			t.Errorf("server.Run error: %v", srvErr)
		}
	}()

	// 1. Default auto scope picks repo v2 shadowing global.
	autoRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "knurl_run",
		Arguments: map[string]any{"name": "dual-tool", "input": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("CallTool(auto): %v", err)
	}
	autoEnv := decodeCallToolEnvelope(t, autoRes)
	if !autoEnv.OK || autoEnv.Scope != contract.ScopeRepo || autoEnv.Version != 2 || autoEnv.Output["tier"] != "repo" {
		t.Fatalf("unexpected auto result: %+v", autoEnv)
	}

	// 2. Explicit version=1 in auto scope loads repo v1.
	v1Res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "knurl_run",
		Arguments: map[string]any{"name": "dual-tool", "version": 1, "input": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("CallTool(version 1): %v", err)
	}
	v1Env := decodeCallToolEnvelope(t, v1Res)
	if !v1Env.OK || v1Env.Scope != contract.ScopeRepo || v1Env.Version != 1 || v1Env.Output["ver"] != float64(1) {
		t.Fatalf("unexpected v1 result: %+v", v1Env)
	}

	// 3. Explicit scope="global" accesses shadowed global tool.
	globalRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "knurl_run",
		Arguments: map[string]any{"name": "dual-tool", "scope": "global", "input": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("CallTool(global): %v", err)
	}
	globalEnv := decodeCallToolEnvelope(t, globalRes)
	if !globalEnv.OK || globalEnv.Scope != contract.ScopeGlobal || globalEnv.Output["tier"] != "global" {
		t.Fatalf("unexpected global result: %+v", globalEnv)
	}

	// 4. Explicit scope="repo" on global-only tool fails with TOOL_NOT_FOUND.
	repoMissRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "knurl_run",
		Arguments: map[string]any{"name": "global-only", "scope": "repo", "input": map[string]any{}},
	})
	if err != nil {
		t.Fatalf("CallTool(repo miss): %v", err)
	}
	missEnv := decodeCallToolEnvelope(t, repoMissRes)
	if missEnv.OK || missEnv.Error == nil || missEnv.Error.Code != contract.ErrToolNotFound {
		t.Fatalf("expected TOOL_NOT_FOUND, got %+v", missEnv)
	}
}
