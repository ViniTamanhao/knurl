package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_worker" {
		os.Exit(runtime.RunWorker(os.Stdin, os.Stdout))
	}
	os.Exit(m.Run())
}

func TestRunCLIStub(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "no args prints help",
			args:       nil,
			wantCode:   0,
			wantStdout: "Usage:",
		},
		{
			name:       "help flag",
			args:       []string{"--help"},
			wantCode:   0,
			wantStdout: "knurl — Tools your agents forge",
		},
		{
			name:       "short help flag",
			args:       []string{"-h"},
			wantCode:   0,
			wantStdout: "Usage:",
		},
		{
			name:       "help subcommand",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: "Usage:",
		},
		{
			name:       "version flag",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: "knurl " + version,
		},
		{
			name:       "short version flag",
			args:       []string{"-v"},
			wantCode:   0,
			wantStdout: "knurl " + version,
		},
		{
			name:       "version subcommand",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "knurl " + version,
		},
		{
			name:       "unknown argument exits 2",
			args:       []string{"--bogus"},
			wantCode:   2,
			wantStderr: "unknown command or flag",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tc.args, &stdout, &stderr)
			if got != tc.wantCode {
				t.Fatalf("run(%v) = %d, want %d (stderr=%q)", tc.args, got, tc.wantCode, stderr.String())
			}
			if tc.wantStdout != "" && !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout %q does not contain %q", stdout.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr %q does not contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}

func TestHelpOmitsHiddenWorker(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("--help exited %d", code)
	}
	if strings.Contains(stdout.String(), "_worker") {
		t.Fatalf("--help must omit hidden _worker subcommand, got:\n%s", stdout.String())
	}
}

func TestRunSubcommandEndToEnd(t *testing.T) {
	workDir := t.TempDir()
	gitCmd := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitCmd("init", "-b", "main")
	gitCmd("config", "user.name", "Test")
	gitCmd("config", "user.email", "test@example.com")
	gitCmd("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Demo\n"), 0o600); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitCmd("add", "README.md")
	gitCmd("commit", "-m", "init")

	examplesDir := filepath.Join("..", "..", "examples")

	// Flags after tool name (default structured output)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"run", "repo-context",
		"--tools-dir", examplesDir,
		"--workdir", workDir,
		"--arg", "recent_commits=1",
		"--arg", "include_tree=false",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("knurl run exited %d: stderr=%s", code, stderr.String())
	}
	var outObj map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &outObj); err != nil {
		t.Fatalf("decode stdout JSON: %v\n%s", err, stdout.String())
	}
	if isGit, _ := outObj["is_git_repo"].(bool); !isGit {
		t.Fatalf("expected is_git_repo=true, got %+v", outObj)
	}
	if tree, _ := outObj["tree_files"].([]any); len(tree) != 0 {
		t.Fatalf("expected empty tree_files when include_tree=false, got %+v", tree)
	}

	// Flags before tool name + --json + --input
	stdout.Reset()
	stderr.Reset()
	code = run([]string{
		"run",
		"--tools-dir", examplesDir,
		"--workdir", workDir,
		"--input", `{"recent_commits":1}`,
		"--json",
		"repo-context",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("knurl run --json exited %d: stderr=%s", code, stderr.String())
	}
	var env contract.ExecutionResult
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("decode --json envelope: %v\n%s", err, stdout.String())
	}
	if !env.OK || env.Tool != "repo-context" || env.Version != 1 {
		t.Fatalf("unexpected envelope: %+v", env)
	}

	// Invalid input rejected cleanly
	stdout.Reset()
	stderr.Reset()
	code = run([]string{
		"run", "repo-context",
		"--tools-dir", examplesDir,
		"--workdir", workDir,
		"--arg", "recent_commits=500",
	}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "INVALID_INPUT") {
		t.Fatalf("expected non-zero + INVALID_INPUT on stderr, got code=%d stderr=%q", code, stderr.String())
	}

	// Invalid input with --json still exits 1 and emits structured ExecutionResult on stderr
	stdout.Reset()
	stderr.Reset()
	code = run([]string{
		"run", "repo-context",
		"--tools-dir", examplesDir,
		"--workdir", workDir,
		"--arg", "recent_commits=500",
		"--json",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 on --json INVALID_INPUT, got %d (stderr=%q)", code, stderr.String())
	}
	var errEnv contract.ExecutionResult
	if err := json.Unmarshal(stderr.Bytes(), &errEnv); err != nil {
		t.Fatalf("decode --json stderr envelope: %v\n%s", err, stderr.String())
	}
	if errEnv.OK || errEnv.Error == nil || errEnv.Error.Code != contract.ErrInvalidInput {
		t.Fatalf("unexpected --json error envelope: %+v", errEnv)
	}

	// Missing tool exits non-zero
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"run", "missing-tool", "--tools-dir", examplesDir}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "TOOL_NOT_FOUND") {
		t.Fatalf("expected non-zero + TOOL_NOT_FOUND, got code=%d stderr=%q", code, stderr.String())
	}

	// MCP help and bad flag
	stdout.Reset()
	stderr.Reset()
	if mcpHelpCode := run([]string{"mcp", "--help"}, &stdout, &stderr); mcpHelpCode != 0 || !strings.Contains(stdout.String(), "knurl mcp") {
		t.Fatalf("mcp --help failed: code=%d stdout=%q", mcpHelpCode, stdout.String())
	}
	if mcpBadCode := run([]string{"mcp", "--bogus"}, &stdout, &stderr); mcpBadCode != 2 {
		t.Fatalf("mcp --bogus code=%d, want 2", mcpBadCode)
	}
}
