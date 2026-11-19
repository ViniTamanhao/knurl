package runtime_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_worker" {
		os.Exit(runtime.RunWorker(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

func baseTestManifest() contract.Manifest {
	return contract.Manifest{
		SchemaVersion: "1",
		Name:          "runtime-test-tool",
		Version:       1,
		Description:   "Runtime subprocess isolation test tool",
		ReadOnly:      true,
		TimeoutMs:     5000,
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
		OutputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"greeting"},
			"properties": map[string]any{
				"greeting": map[string]any{"type": "string"},
			},
		},
		Capabilities: contract.Capabilities{},
	}
}

func TestWorkerHappyPathAndDiagnostics(t *testing.T) {
	sup := &runtime.Supervisor{}
	m := baseTestManifest()
	m.Secrets = []contract.SecretRef{
		{Name: "API_SECRET", Required: true, Description: "Test secret"},
	}
	sup.EnvLookup = func(k string) (string, bool) {
		if k == "API_SECRET" {
			return "super-secret-xyz", true
		}
		return os.LookupEnv(k)
	}

	source := `
function main(input) {
  knurl.log("running with secret super-secret-xyz");
  return { greeting: "hello " + (input.name || "world") + " leak=super-secret-xyz" };
}
`
	res := sup.ExecuteBundle(context.Background(), m, source, t.TempDir(), map[string]any{"name": "knurl"}, 0, contract.ScopeRepo)
	if !res.OK {
		t.Fatalf("ExecuteBundle failed: %+v", res.Error)
	}
	got, _ := res.Output["greeting"].(string)
	if got != "hello knurl leak=[REDACTED:API_SECRET]" {
		t.Fatalf("greeting = %q, want redacted greeting", got)
	}
	if len(res.Diagnostics) != 1 || res.Diagnostics[0] != "running with secret [REDACTED:API_SECRET]" {
		t.Fatalf("diagnostics = %v, want redacted line", res.Diagnostics)
	}
}

func TestWorkerInfiniteLoopAndChildProcessCleanup(t *testing.T) {
	sup := &runtime.Supervisor{}

	// 1. Infinite JS loop killed promptly by timeout
	m := baseTestManifest()
	start := time.Now()
	res := sup.ExecuteBundle(context.Background(), m, `function main() { while (true) {} }`, t.TempDir(), nil, 150, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrTimeout {
		t.Fatalf("expected TIMEOUT on infinite loop, got ok=%v err=%+v", res.OK, res.Error)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("infinite loop took too long to terminate: %v", elapsed)
	}

	// 2. Child process spawned via knurl.exec is killed when worker times out (no orphan hanging pipes)
	helperBin := filepath.Join(t.TempDir(), "sleep-helper")
	helperSrc := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(helperSrc, []byte("package main\nimport \"time\"\nfunc main() { time.Sleep(30 * time.Second) }\n"), 0o600); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if out, buildErr := exec.Command("go", "build", "-o", helperBin, helperSrc).CombinedOutput(); buildErr != nil {
		t.Fatalf("build helper: %v\n%s", buildErr, out)
	}
	t.Setenv("PATH", filepath.Dir(helperBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	execManifest := baseTestManifest()
	execManifest.Capabilities.Exec = &contract.ExecCapability{
		Templates: []contract.ExecTemplate{
			{
				ID:          "long-sleep",
				Program:     "sleep-helper",
				Args:        []string{"--wait"},
				ReadOnly:    true,
				Description: "Sleep 30s to test process group kill",
			},
		},
	}

	start = time.Now()
	res = sup.ExecuteBundle(context.Background(), execManifest, `function main() { knurl.exec("long-sleep"); return { greeting: "done" }; }`, t.TempDir(), nil, 150, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrTimeout {
		t.Fatalf("expected TIMEOUT on slow child exec, got ok=%v err=%+v", res.OK, res.Error)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("worker timeout with child took too long: %v", elapsed)
	}
}

func TestWorkerOutputCapsAndErrorClassification(t *testing.T) {
	sup := &runtime.Supervisor{}
	m := baseTestManifest()

	// Pre-spawn invalid input rejection
	res := sup.ExecuteBundle(context.Background(), m, `function main() { while(true){} }`, t.TempDir(), map[string]any{"unknown": 1}, 0, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrInvalidInput {
		t.Fatalf("expected INVALID_INPUT before spawn, got ok=%v err=%+v", res.OK, res.Error)
	}

	// Oversize JSON return triggers LIMIT_EXCEEDED
	res = sup.ExecuteBundle(context.Background(), m, `function main() { return { greeting: "x".repeat(3 * 1024 * 1024) }; }`, t.TempDir(), nil, 0, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrLimitExceeded {
		t.Fatalf("expected LIMIT_EXCEEDED on >2MiB output, got ok=%v err=%+v", res.OK, res.Error)
	}

	// JS uncaught throw -> TOOL_ERROR
	res = sup.ExecuteBundle(context.Background(), m, `function main() { throw new Error("boom"); }`, t.TempDir(), nil, 0, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrToolError || !strings.Contains(res.Error.Message, "boom") {
		t.Fatalf("expected TOOL_ERROR(boom), got ok=%v err=%+v", res.OK, res.Error)
	}

	// Missing main -> COMPILE_ERROR
	res = sup.ExecuteBundle(context.Background(), m, `function helper() { return {}; }`, t.TempDir(), nil, 0, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrCompileError {
		t.Fatalf("expected COMPILE_ERROR on missing main, got ok=%v err=%+v", res.OK, res.Error)
	}

	// async function main -> COMPILE_ERROR
	res = sup.ExecuteBundle(context.Background(), m, `async function main() { return { greeting: "hi" }; }`, t.TempDir(), nil, 0, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrCompileError {
		t.Fatalf("expected COMPILE_ERROR on async main, got ok=%v err=%+v", res.OK, res.Error)
	}

	// Invalid return value against output_schema -> INVALID_OUTPUT
	res = sup.ExecuteBundle(context.Background(), m, `function main() { return { wrong_field: 1 }; }`, t.TempDir(), nil, 0, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrInvalidOutput {
		t.Fatalf("expected INVALID_OUTPUT, got ok=%v err=%+v", res.OK, res.Error)
	}

	// Non-object return value -> INVALID_OUTPUT
	res = sup.ExecuteBundle(context.Background(), m, `function main() { return [1, 2, 3]; }`, t.TempDir(), nil, 0, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrInvalidOutput {
		t.Fatalf("expected INVALID_OUTPUT for array return, got ok=%v err=%+v", res.OK, res.Error)
	}

	// Unsupported Node/async globals fail cleanly
	for _, expr := range []string{
		`console.log("hi");`,
		`require("fs");`,
		`setTimeout(function(){}, 10);`,
		`fetch("https://example.com");`,
		`new Promise(function(){});`,
		`Promise.resolve(1);`,
	} {
		src := `function main() { ` + expr + ` return { greeting: "no" }; }`
		badRes := sup.ExecuteBundle(context.Background(), m, src, t.TempDir(), nil, 0, contract.ScopeRepo)
		if badRes.OK || badRes.Error == nil || badRes.Error.Code != contract.ErrToolError {
			t.Fatalf("expected TOOL_ERROR for %q, got ok=%v err=%+v", expr, badRes.OK, badRes.Error)
		}
	}
}

func TestWorkerDoesNotInheritArbitraryEnv(t *testing.T) {
	helperBin := filepath.Join(t.TempDir(), "env-probe")
	helperSrc := filepath.Join(t.TempDir(), "main.go")
	helperCode := `package main
import (
	"fmt"
	"os"
)
func main() {
	fmt.Printf("secret_present=%v", os.Getenv("ARBITRARY_PARENT_SECRET") != "")
}
`
	if err := os.WriteFile(helperSrc, []byte(helperCode), 0o600); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if out, buildErr := exec.Command("go", "build", "-o", helperBin, helperSrc).CombinedOutput(); buildErr != nil {
		t.Fatalf("build helper: %v\n%s", buildErr, out)
	}
	t.Setenv("PATH", filepath.Dir(helperBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARBITRARY_PARENT_SECRET", "must-not-enter-worker")

	m := baseTestManifest()
	m.Capabilities.Exec = &contract.ExecCapability{
		Templates: []contract.ExecTemplate{
			{
				ID:          "probe-env",
				Program:     "env-probe",
				Args:        []string{"check"},
				ReadOnly:    true,
				Description: "Check whether worker inherited arbitrary env vars",
			},
		},
	}

	sup := &runtime.Supervisor{}
	res := sup.ExecuteBundle(context.Background(), m, `
function main() {
  var r = knurl.exec("probe-env");
  return { greeting: r.stdout };
}
`, t.TempDir(), nil, 0, contract.ScopeRepo)
	if !res.OK {
		t.Fatalf("ExecuteBundle failed: %+v", res.Error)
	}
	if got := res.Output["greeting"]; got != "secret_present=false" {
		t.Fatalf("greeting = %v, want secret_present=false", got)
	}
}
