package approval_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ViniTamanhao/knurl/internal/approval"
	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_worker" {
		os.Exit(runtime.RunWorker(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}
func setup(t *testing.T) *approval.Store {
	t.Helper()
	work := t.TempDir()
	if err := os.Mkdir(filepath.Join(work, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &approval.Store{Registry: &registry.Registry{WorkDir: work, GlobalDir: filepath.Join(t.TempDir(), "tools")}, WorkDir: work, Supervisor: &runtime.Supervisor{}}
}
func request() contract.ProposeRequest {
	return contract.ProposeRequest{Name: "sample-tool", Scope: contract.ScopeRepo, Summary: "add a sample tool", Manifest: contract.Manifest{SchemaVersion: "1", Name: "sample-tool", Version: 1, Description: "A sample test tool", ReadOnly: true, InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}}, Source: `function main() { return {value: 1}; }`, Tests: contract.TestSuite{SchemaVersion: "1", Cases: []contract.TestCase{{Name: "sample-case", Input: map[string]any{}, Expect: contract.TestExpectation{OK: true, Output: map[string]any{"value": 1}}}}}}
}
func propose(t *testing.T, s *approval.Store, r contract.ProposeRequest) contract.ProposeResponse {
	t.Helper()
	p, err := s.Propose(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func approve(t *testing.T, s *approval.Store, id string) {
	t.Helper()
	p, _, err := s.Review(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Approve(id, p.BundleSHA256); err != nil {
		t.Fatal(err)
	}
}

func TestStagingValidationAndRejection(t *testing.T) {
	s := setup(t)
	r := request()
	p := propose(t, s, r)
	if !p.DryRun.Executed || !p.DryRun.OK {
		t.Fatalf("preview: %+v", p.DryRun)
	}
	if _, err := s.Registry.Resolve(r.Name, contract.ScopeAuto); err == nil {
		t.Fatal("unapproved tool resolved")
	}
	if _, err := s.Reject(p.ProposalID, "needs better coverage"); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(p.ProposalID)
	if err != nil || status.Status != contract.ProposalRejected || status.DecisionNote != "needs better coverage" {
		t.Fatalf("%+v %v", status, err)
	}
	if _, err = s.Approve(p.ProposalID, ""); err == nil {
		t.Fatal("approved rejected proposal")
	}
}

func TestInvalidSubmissionsLeaveNoPendingProposals(t *testing.T) {
	for _, name := range []string{"syntax", "missing-main", "fake-main", "async-main", "failing-test", "manifest", "tests", "traversal", "scope"} {
		t.Run(name, func(t *testing.T) {
			s := setup(t)
			r := request()
			switch name {
			case "syntax":
				r.Source = "function main( {"
			case "missing-main":
				r.Source = "var other=1;"
			case "fake-main":
				r.Source = `var text="function main(){}";`
			case "async-main":
				r.Source = `async function main(){return {};}`
			case "failing-test":
				r.Source = `function main(){return {};}`
			case "manifest":
				r.Manifest.SchemaVersion = "9"
			case "tests":
				r.Tests.Cases = nil
			case "traversal":
				r.Name = "../escape"
			case "scope":
				r.Scope = contract.ScopeAuto
			}
			if _, err := s.Propose(context.Background(), r); err == nil {
				t.Fatal("accepted invalid proposal")
			}
			list, err := s.List(true)
			if err != nil || len(list) != 0 {
				t.Fatalf("invalid proposal persisted: %+v %v", list, err)
			}
		})
	}
}

func TestStaleBaseAndTamperedBundle(t *testing.T) {
	s := setup(t)
	r := request()
	p := propose(t, s, r)
	other := propose(t, s, r)
	approve(t, s, p.ProposalID)
	review, _, err := s.Review(other.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Approve(other.ProposalID, review.BundleSHA256); err == nil {
		t.Fatal("accepted stale proposal")
	}
	if _, err = s.Propose(context.Background(), r); err == nil {
		t.Fatal("accepted stale target")
	}
	r.Manifest.Version = 2
	base := 0
	r.BaseVersion = &base
	if _, err = s.Propose(context.Background(), r); err == nil {
		t.Fatal("accepted stale explicit base")
	}
	base = 1
	p = propose(t, s, r)
	dir := filepath.Join(filepath.Dir(s.Registry.GlobalDir), "staging", p.ProposalID)
	if err = os.WriteFile(filepath.Join(dir, "tool.js"), []byte(`function main(){return {value:9};}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Review(p.ProposalID); err == nil {
		t.Fatal("accepted altered bytes")
	}
	for _, id := range []string{"../escape", "prop_../../escape", "/tmp/x", ""} {
		if _, err = s.Read(id); err == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
}

func TestElevatedFixtureDoesNotExecuteLiveCommands(t *testing.T) {
	s := setup(t)
	r := request()
	r.Manifest.Capabilities.Exec = &contract.ExecCapability{Templates: []contract.ExecTemplate{{ID: "write-marker", Program: "touch", Args: []string{"marker"}, ReadOnly: true, Description: "Test marker command"}}}
	r.Source = `function main(){knurl.exec("write-marker",{});return {value:1};}`
	r.Tests.Cases[0].Mocks = &contract.TestMocks{Exec: []contract.ExecMock{{TemplateID: "write-marker", Result: &contract.ExecMockResult{ExitCode: 0}}}}
	p := propose(t, s, r)
	if p.DryRun.Executed || p.DryRun.SkippedReason == "" {
		t.Fatal("elevated dry-run ran")
	}
	if _, err := os.Stat(filepath.Join(s.WorkDir, "marker")); !os.IsNotExist(err) {
		t.Fatal("pre-approval side effect")
	}
	r.Tests.Cases[0].Mocks = nil
	if _, err := s.Propose(context.Background(), r); err == nil {
		t.Fatal("unmocked exec escaped fixture mode")
	}
}

func TestCapabilityEscalationAndFixtureRedaction(t *testing.T) {
	s := setup(t)
	r := request()
	p := propose(t, s, r)
	approve(t, s, p.ProposalID)
	r.Manifest.Version = 2
	r.Manifest.Secrets = []contract.SecretRef{{Name: "API_TOKEN", Required: true, Description: "Example token"}}
	r.Manifest.Capabilities.HTTP = &contract.HTTPCapability{Allow: []contract.HTTPRule{{Domain: "api.example.com", Methods: []string{"GET"}, Secrets: []string{"API_TOKEN"}}}}
	r.Manifest.Capabilities.Exec = &contract.ExecCapability{Templates: []contract.ExecTemplate{{ID: "echo-token", Program: "echo", Args: []string{"token", "${secret:API_TOKEN}"}, ReadOnly: true, Description: "Test secret redaction"}}}
	r.Source = `function main(){var x=knurl.exec("echo-token",{});knurl.log(x.stdout);return {value:x.stdout};}`
	r.Tests.Cases[0].Mocks = &contract.TestMocks{Secrets: []string{"API_TOKEN"}, Exec: []contract.ExecMock{{TemplateID: "echo-token", Result: &contract.ExecMockResult{Stdout: "knurl-fixture-secret-API_TOKEN"}}}}
	r.Tests.Cases[0].Expect.Output = map[string]any{"value": "[REDACTED:API_TOKEN]"}
	p = propose(t, s, r)
	if !p.CapabilityDiff.HasEscalation || p.CapabilityDiff.ElevatedBefore || !p.CapabilityDiff.ElevatedAfter {
		t.Fatalf("diff %+v", p.CapabilityDiff)
	}
	record, err := s.Read(p.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	if record.SourceDiff == "" {
		t.Fatal("missing source diff")
	}
	raw, _ := json.Marshal(record)
	if strings.Contains(string(raw), "knurl-fixture-secret-") {
		t.Fatal("fixture secret leaked")
	}
	for _, capability := range []string{"exec.template", "http.allow", "secret"} {
		found := false
		for _, change := range p.CapabilityDiff.Changes {
			if change.Kind == "added" && change.Capability == capability {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing escalation %s", capability)
		}
	}
}

func TestFlagshipFixtures(t *testing.T) {
	for _, name := range []string{"repo-context", "vps-health", "pr-ready"} {
		t.Run(name, func(t *testing.T) {
			s := setup(t)
			dir := filepath.Join("..", "..", "examples", name)
			m, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := contract.ValidateManifestJSON(m)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "tests.json"))
			if err != nil {
				t.Fatal(err)
			}
			tests, err := contract.ValidateTestsJSON(data, manifest)
			if err != nil {
				t.Fatal(err)
			}
			source, err := os.ReadFile(filepath.Join(dir, "tool.js"))
			if err != nil {
				t.Fatal(err)
			}
			r := contract.ProposeRequest{Name: name, Scope: contract.ScopeRepo, Summary: "validate flagship fixtures", Manifest: manifest, Tests: tests, Source: string(source)}
			propose(t, s, r)
		})
	}
}

func TestNoSafePreviewInput(t *testing.T) {
	s := setup(t)
	r := request()
	r.Manifest.InputSchema = map[string]any{"type": "object", "required": []string{"value"}, "properties": map[string]any{"value": map[string]any{"type": "integer"}}}
	r.Tests.Cases[0].Expect = contract.TestExpectation{OK: false, ErrorCode: contract.ErrInvalidInput}
	p := propose(t, s, r)
	if p.DryRun.Executed || !strings.Contains(p.DryRun.SkippedReason, "input") {
		t.Fatalf("preview %+v", p.DryRun)
	}
}

func TestUnifiedSourceDiff(t *testing.T) {
	diff := approval.SourceDiff("one\ntwo\nthree\n", "one\nchanged\nthree\n")
	if !strings.Contains(diff, " one\n-two\n+changed\n three\n") {
		t.Fatal(diff)
	}
	if diff = approval.SourceDiff("same", "same\n"); !strings.Contains(diff, "No newline at end of file") {
		t.Fatal(diff)
	}
	if diff = approval.SourceDiff("", "added\n"); !strings.Contains(diff, "@@ -0,0 +1,1 @@") {
		t.Fatal(diff)
	}
}
