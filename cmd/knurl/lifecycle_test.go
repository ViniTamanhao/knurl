package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ViniTamanhao/knurl/internal/approval"
	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/mcpserver"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func lifecycleClient(t *testing.T, work string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	server := mcpserver.NewServer(mcpserver.Config{WorkDir: work})
	a, b := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, a) }()
	client := mcp.NewClient(&mcp.Implementation{Name: t.Name(), Version: "1"}, nil)
	session, err := client.Connect(ctx, b, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	return session
}
func callLifecycle[T any](t *testing.T, client *mcp.ClientSession, name string, args any, wantError bool) T {
	t.Helper()
	res, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError != wantError {
		t.Fatalf("%s IsError=%t content=%+v structured=%+v", name, res.IsError, res.Content, res.StructuredContent)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %s: %v", name, raw, err)
	}
	return out
}
func lifecycleCLI(t *testing.T, input string, args ...string) string {
	t.Helper()
	var out, err bytes.Buffer
	if code := runWithStdin(args, strings.NewReader(input), &out, &err); code != 0 {
		t.Fatalf("CLI %v exit=%d: %s", args, code, err.String())
	}
	return out.String()
}
func flagshipRequest(t *testing.T) contract.ProposeRequest {
	t.Helper()
	dir := filepath.Join("..", "..", "examples", "pr-ready")
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := contract.ValidateManifestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "tests.json"))
	if err != nil {
		t.Fatal(err)
	}
	tests, err := contract.ValidateTestsJSON(data, m)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "tool.js"))
	if err != nil {
		t.Fatal(err)
	}
	return contract.ProposeRequest{Name: m.Name, Scope: contract.ScopeRepo, Summary: "add project PR readiness checks", Manifest: m, Source: string(data), Tests: tests}
}
func lifecycleWorkspace(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	t.Setenv("KNURL_HOME", t.TempDir())
	t.Setenv("KNURL_TOOLS_DIR", "")
	cmdGit(t, work, "init", "-b", "main")
	t.Chdir(work)
	return work
}
func cmdGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %v: %s", err, out)
	}
}

func TestCrossAgentLifecycle(t *testing.T) {
	req := flagshipRequest(t)
	work := lifecycleWorkspace(t)
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(work, ".pr-ready.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"schema_version":1}`)
	a := lifecycleClient(t, work)
	tools, err := a.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || tool.Annotations.OpenWorldHint == nil {
			t.Fatalf("missing annotations: %+v", tool)
		}
	}
	for _, name := range []string{"knurl_discover", "knurl_inspect", "knurl_run", "knurl_propose", "knurl_status"} {
		if !names[name] {
			t.Fatalf("missing %s", name)
		}
	}
	if len(names) != 5 {
		t.Fatalf("unexpected MCP tools %+v", names)
	}
	p := callLifecycle[contract.ProposeResponse](t, a, "knurl_propose", req, false)
	if p.Status != contract.ProposalPending || p.Validation.TestsPassed != 3 || p.DryRun.Executed {
		t.Fatalf("proposal %+v", p)
	}
	invisible := callLifecycle[contract.DiscoverResponse](t, a, "knurl_discover", map[string]any{}, false)
	if len(invisible.Tools) != 0 {
		t.Fatal("discovered unapproved tool")
	}
	runReq := contract.RunRequest{Name: req.Name, Input: map[string]any{"allow_dirty": true}}
	missing := callLifecycle[contract.RunResponse](t, a, "knurl_run", runReq, true)
	if missing.Error.Code != contract.ErrToolNotFound {
		t.Fatalf("unapproved run: %+v", missing)
	}
	cancelled := lifecycleCLI(t, "\n", "approve", "--last")
	if !strings.Contains(cancelled, "cancelled") {
		t.Fatal("approval did not default to no")
	}
	review := lifecycleCLI(t, "y\n", "approve", "--last")
	for _, text := range []string{"ELEVATED", "ADDED exec.template", "Fixtures: 3/3 passed", "Source:", "Approve? [y/N]"} {
		if !strings.Contains(review, text) {
			t.Fatalf("review missing %s: %s", text, review)
		}
	}
	status := callLifecycle[contract.StatusResponse](t, a, "knurl_status", contract.StatusRequest{ProposalID: p.ProposalID}, false)
	if status.Status != contract.ProposalApproved || status.PromotedVersion != 1 || status.DecidedAt == nil {
		t.Fatalf("status %+v", status)
	}
	b := lifecycleClient(t, work)
	found := callLifecycle[contract.DiscoverResponse](t, b, "knurl_discover", map[string]any{"query": "readiness", "tags": []string{"pr"}, "scope": "all"}, false)
	if len(found.Tools) != 1 || found.Tools[0].Version != 1 {
		t.Fatalf("discover %+v", found)
	}
	inspected := callLifecycle[contract.InspectResponse](t, b, "knurl_inspect", contract.InspectRequest{Name: req.Name}, false)
	if inspected.Version != 1 || inspected.Manifest.InputSchema == nil || len(inspected.AvailableVersions) != 1 {
		t.Fatalf("inspect %+v", inspected)
	}
	first := callLifecycle[contract.RunResponse](t, b, "knurl_run", runReq, false)
	if first.Version != 1 || first.Output["ready"] != true {
		t.Fatalf("run %+v", first)
	}
	write(`{"schema_version":2}`)
	broken := callLifecycle[contract.RunResponse](t, b, "knurl_run", runReq, true)
	if broken.Error.Code != contract.ErrToolError || !strings.Contains(broken.Error.Message, "schema_version") {
		t.Fatalf("failure context %+v", broken)
	}
	req.Manifest.Version = 2
	base := 1
	req.BaseVersion = &base
	req.Summary = "accept the new project config version"
	req.Source = strings.ReplaceAll(req.Source, "config.schema_version !== 1", "config.schema_version !== 1 && config.schema_version !== 2")
	req.Tests.Cases[2].Mocks.FS.Files[".pr-ready.json"] = `{"schema_version":3}`
	repair := callLifecycle[contract.ProposeResponse](t, b, "knurl_propose", req, false)
	store := &approval.Store{Registry: &registry.Registry{WorkDir: work}, WorkDir: work}
	record, err := store.Read(repair.ProposalID)
	if err != nil || !strings.Contains(record.SourceDiff, "--- a/tool.js") || len(record.CapabilityDiff.Changes) == 0 {
		t.Fatalf("repair diff %+v %v", record, err)
	}
	still := callLifecycle[contract.InspectResponse](t, b, "knurl_inspect", contract.InspectRequest{Name: req.Name}, false)
	if still.Version != 1 {
		t.Fatal("repair activated before approval")
	}
	review = lifecycleCLI(t, "", "approve", "--last", "--yes")
	if !strings.Contains(review, "repair v1 -> v2") || !strings.Contains(review, "Code diff:") {
		t.Fatal(review)
	}
	t.Logf("Approval review sample:\n%s", review)
	repaired := callLifecycle[contract.RunResponse](t, b, "knurl_run", runReq, false)
	if repaired.Version != 2 || repaired.Output["ready"] != true {
		t.Fatalf("repair run %+v", repaired)
	}
	rollback := lifecycleCLI(t, "", "rollback", req.Name)
	if !strings.Contains(rollback, "v2 -> v1") {
		t.Fatal(rollback)
	}
	rolled := callLifecycle[contract.RunResponse](t, b, "knurl_run", runReq, true)
	if rolled.Version != 1 {
		t.Fatalf("rollback %+v", rolled)
	}
	if _, err = os.Stat(filepath.Join(work, ".knurl", "tools", req.Name, "v2", "tool.js")); err != nil {
		t.Fatal("rollback removed history", err)
	}
	lifecycleCLI(t, "", "rollback", req.Name, "--to", "v2", "--scope", "repo")
}

func TestCLIRejectionAndProposals(t *testing.T) {
	req := flagshipRequest(t)
	work := lifecycleWorkspace(t)
	client := lifecycleClient(t, work)
	p := callLifecycle[contract.ProposeResponse](t, client, "knurl_propose", req, false)
	list := lifecycleCLI(t, "", "proposals", "--json")
	if !strings.Contains(list, p.ProposalID) {
		t.Fatal(list)
	}
	lifecycleCLI(t, "", "reject", "--last", "--reason", "please narrow permissions")
	status := callLifecycle[contract.StatusResponse](t, client, "knurl_status", contract.StatusRequest{ProposalID: p.ProposalID}, false)
	if status.Status != contract.ProposalRejected || status.DecisionNote != "please narrow permissions" {
		t.Fatalf("status %+v", status)
	}
	list = lifecycleCLI(t, "", "proposals", "--json")
	if strings.TrimSpace(list) != "[]" {
		t.Fatal(list)
	}
	list = lifecycleCLI(t, "", "proposals", "--status", "all")
	if !strings.Contains(list, "rejected") {
		t.Fatal(list)
	}
	reg := &registry.Registry{WorkDir: work}
	if _, err := reg.Resolve(req.Name, contract.ScopeRepo); err == nil {
		t.Fatal("rejection changed registry")
	}
}

func TestLifecycleCLIInvalidFlags(t *testing.T) {
	for _, args := range [][]string{{"approve"}, {"approve", "some-id", "--last"}, {"reject", "--yes"}, {"proposals", "--status", "approved"}, {"rollback", "sample-tool", "--scope", "auto"}, {"rollback", "sample-tool", "--to", "v0"}} {
		var out, err bytes.Buffer
		if code := runWithStdin(args, strings.NewReader(""), &out, &err); code != 2 {
			t.Fatalf("%v exit=%d stderr=%s", args, code, err.String())
		}
	}
}

func TestMCPRejectsUnknownBundleFields(t *testing.T) {
	req := flagshipRequest(t)
	work := lifecycleWorkspace(t)
	client := lifecycleClient(t, work)
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]any
	if err = json.Unmarshal(data, &args); err != nil {
		t.Fatal(err)
	}
	manifest := args["manifest"].(map[string]any)
	manifest["unreviewed_permission"] = true
	out := callLifecycle[map[string]any](t, client, "knurl_propose", args, true)
	toolErr := out["error"].(map[string]any)
	if toolErr["code"] != "INVALID_MANIFEST" {
		t.Fatalf("%+v", out)
	}
	delete(manifest, "unreviewed_permission")
	args["tests"].(map[string]any)["unknown"] = true
	out = callLifecycle[map[string]any](t, client, "knurl_propose", args, true)
	toolErr = out["error"].(map[string]any)
	if toolErr["code"] != "INVALID_TESTS" {
		t.Fatalf("%+v", out)
	}
}

func TestMCPStdioLifecycleKeepsProtocolClean(t *testing.T) {
	req := flagshipRequest(t)
	work := lifecycleWorkspace(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "_mcp-test", work)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 5 {
		t.Fatalf("tools %+v", tools)
	}
	p := callLifecycle[contract.ProposeResponse](t, session, "knurl_propose", req, false)
	if p.Validation.TestsPassed != 3 {
		t.Fatalf("proposal %+v", p)
	}
	status := callLifecycle[contract.StatusResponse](t, session, "knurl_status", contract.StatusRequest{ProposalID: p.ProposalID}, false)
	if status.Status != contract.ProposalPending {
		t.Fatalf("status %+v", status)
	}
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected MCP diagnostics: %s", stderr.String())
	}
}
