package runtime_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

func loadExampleBundleForRuntimeTest(t *testing.T, name string) (contract.Manifest, string) {
	t.Helper()
	bundleDir := filepath.Join("..", "..", "examples", name)
	manifestBytes, err := os.ReadFile(filepath.Join(bundleDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read %s manifest.json: %v", name, err)
	}
	manifest, valErr := contract.ValidateManifestJSON(manifestBytes)
	if valErr != nil {
		t.Fatalf("validate %s manifest.json: %v", name, valErr)
	}
	srcBytes, err := os.ReadFile(filepath.Join(bundleDir, "tool.js"))
	if err != nil {
		t.Fatalf("read %s tool.js: %v", name, err)
	}
	return manifest, string(srcBytes)
}

func runGitExampleTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFileExampleTest(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func TestExampleRepoContextGitRepo(t *testing.T) {
	manifest, src := loadExampleBundleForRuntimeTest(t, "repo-context")

	workDir := t.TempDir()
	runGitExampleTest(t, workDir, "init", "-b", "main")
	runGitExampleTest(t, workDir, "config", "user.name", "Test User")
	runGitExampleTest(t, workDir, "config", "user.email", "test@example.com")
	runGitExampleTest(t, workDir, "config", "commit.gpgsign", "false")

	writeFileExampleTest(t, workDir, "go.mod", "module example.com/demo\n\ngo 1.27\n")
	writeFileExampleTest(t, workDir, "README.md", "# Demo Repo\n\nIntegration test repository.\n")
	writeFileExampleTest(t, workDir, "cmd/app/main.go", "package main\nfunc main() {}\n")
	runGitExampleTest(t, workDir, "add", "go.mod", "README.md", "cmd/app/main.go")
	runGitExampleTest(t, workDir, "commit", "-m", "feat: initial repo commit")

	// Staged, unstaged, and untracked changes
	writeFileExampleTest(t, workDir, "staged.txt", "staged file\n")
	runGitExampleTest(t, workDir, "add", "staged.txt")
	writeFileExampleTest(t, workDir, "cmd/app/main.go", "package main\n// changed\nfunc main() {}\n")
	writeFileExampleTest(t, workDir, "notes.md", "# Notes\n")

	sup := &runtime.Supervisor{}
	res := sup.ExecuteBundle(context.Background(), manifest, src, workDir, map[string]any{
		"include_tree":           true,
		"max_tree_files":         100,
		"recent_commits":         5,
		"include_readme_excerpt": true,
	}, 0, contract.ScopeRepo)
	if !res.OK {
		t.Fatalf("repo-context failed: %+v", res.Error)
	}

	if isGit, _ := res.Output["is_git_repo"].(bool); !isGit {
		t.Fatalf("expected is_git_repo=true, got %+v", res.Output)
	}
	if manifests, _ := res.Output["manifests"].([]any); !slices.Contains(manifests, any("go.mod")) {
		t.Fatalf("expected go.mod in manifests, got %v", manifests)
	}
	if readme, _ := res.Output["readme_excerpt"].(string); !strings.Contains(readme, "Demo Repo") {
		t.Fatalf("unexpected readme_excerpt: %q", readme)
	}

	langs, _ := res.Output["languages"].([]any)
	foundGo := false
	foundMD := false
	for _, item := range langs {
		entry, _ := item.(map[string]any)
		if entry["name"] == "Go" {
			foundGo = true
		}
		if entry["name"] == "Markdown" {
			foundMD = true
		}
	}
	if !foundGo || !foundMD {
		t.Fatalf("expected Go and Markdown in languages, got %+v", langs)
	}

	gitMap, ok := res.Output["git"].(map[string]any)
	if !ok || gitMap == nil {
		t.Fatalf("expected non-nil git summary, got %+v", res.Output["git"])
	}
	if gitMap["branch"] != "main" {
		t.Fatalf("git.branch = %v, want main", gitMap["branch"])
	}
	if gitMap["staged_count"] != float64(1) {
		t.Fatalf("git.staged_count = %v, want 1", gitMap["staged_count"])
	}
	if gitMap["unstaged_count"] != float64(1) {
		t.Fatalf("git.unstaged_count = %v, want 1", gitMap["unstaged_count"])
	}
	if gitMap["untracked_count"] != float64(1) {
		t.Fatalf("git.untracked_count = %v, want 1", gitMap["untracked_count"])
	}
	if commits, _ := gitMap["recent_commits"].([]any); len(commits) != 1 {
		t.Fatalf("expected 1 recent commit, got %+v", commits)
	}
	if diffFiles, _ := gitMap["diff_files"].([]any); len(diffFiles) != 1 {
		t.Fatalf("expected 1 unstaged diff file, got %+v", diffFiles)
	}
}

func TestExampleRepoContextNonGitDir(t *testing.T) {
	manifest, src := loadExampleBundleForRuntimeTest(t, "repo-context")

	workDir := t.TempDir()
	writeFileExampleTest(t, workDir, "mise.toml", "[tools]\ngo = \"1.27.4\"\n")
	writeFileExampleTest(t, workDir, "scripts/check.sh", "#!/bin/sh\necho ok\n")

	sup := &runtime.Supervisor{}
	res := sup.ExecuteBundle(context.Background(), manifest, src, workDir, map[string]any{
		"include_tree":           false,
		"include_readme_excerpt": false,
	}, 0, contract.ScopeRepo)
	if !res.OK {
		t.Fatalf("repo-context non-git failed: %+v", res.Error)
	}
	if isGit, _ := res.Output["is_git_repo"].(bool); isGit {
		t.Fatalf("expected is_git_repo=false")
	}
	if res.Output["git"] != nil {
		t.Fatalf("expected git=nil, got %+v", res.Output["git"])
	}
	if manifests, _ := res.Output["manifests"].([]any); !slices.Equal(manifests, []any{"mise.toml"}) {
		t.Fatalf("manifests = %v, want [mise.toml]", manifests)
	}
}

func TestExampleVPSHealthWithStubSSH(t *testing.T) {
	manifest, src := loadExampleBundleForRuntimeTest(t, "vps-health")

	const secretKeyPath = "/fake/secret/id_ed25519_supersecret"

	// Compile safe local ssh stub binary
	stubDir := t.TempDir()
	stubBin := filepath.Join(stubDir, "ssh")
	stubSrc := filepath.Join(stubDir, "main.go")
	stubCode := `package main
import (
	"fmt"
	"os"
	"strings"
)
func main() {
	if os.Getenv("VPS_SSH_KEY_PATH") != "" {
		fmt.Fprintln(os.Stderr, "unexpected secret env leak into ssh child")
		os.Exit(99)
	}
	keySeen := false
	for i := 0; i < len(os.Args)-1; i++ {
		if os.Args[i] == "-i" && os.Args[i+1] == "` + secretKeyPath + `" {
			keySeen = true
			break
		}
	}
	if !keySeen {
		fmt.Fprintln(os.Stderr, "expected -i secret key binding in ssh argv")
		os.Exit(98)
	}

	joined := strings.Join(os.Args[1:], " ")
	if strings.Contains(joined, "fail-leak.example.com") {
		fmt.Fprintln(os.Stderr, "Permission denied reading ` + secretKeyPath + `")
		os.Exit(255)
	}

	last := os.Args[len(os.Args)-1]
	if strings.HasPrefix(last, "uname -srm") {
		fmt.Print("Linux 6.8.0-45-generic x86_64\n 14:25:00 up 2 days,  1:00,  0 users,  load average: 1.25, 0.90, 0.70\n               total        used        free      shared  buff/cache   available\nMem:            2048        1024         512          12         512        1000\nSwap:              0           0           0\nFilesystem     1024-blocks      Used Available Capacity Mounted on\n/dev/vda1         41152832  36214492   2828228      88% /\n")
		return
	}
	if len(os.Args) >= 4 && os.Args[len(os.Args)-4] == "systemctl" {
		svc := last
		if svc == "nginx" {
			fmt.Println("active")
			return
		}
		fmt.Println("inactive")
		os.Exit(3)
	}
	fmt.Fprintln(os.Stderr, "unexpected ssh stub command:", joined)
	os.Exit(2)
}
`
	if err := os.WriteFile(stubSrc, []byte(stubCode), 0o600); err != nil {
		t.Fatalf("write ssh stub: %v", err)
	}
	if out, buildErr := exec.Command("go", "build", "-o", stubBin, stubSrc).CombinedOutput(); buildErr != nil {
		t.Fatalf("build ssh stub: %v\n%s", buildErr, out)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workDir := t.TempDir()

	// 1. Missing required secret fails closed before worker spawn
	t.Setenv("VPS_SSH_KEY_PATH", "")
	sup := &runtime.Supervisor{}
	missingRes := sup.ExecuteBundle(context.Background(), manifest, src, workDir, map[string]any{
		"host": "198.51.100.10",
		"user": "deploy",
	}, 0, contract.ScopeGlobal)
	if missingRes.OK || missingRes.Error == nil || missingRes.Error.Code != contract.ErrSecretMissing {
		t.Fatalf("expected SECRET_MISSING, got ok=%v err=%+v", missingRes.OK, missingRes.Error)
	}
	if missingRes.Error.Details["secret"] != "VPS_SSH_KEY_PATH" {
		t.Fatalf("expected details.secret=VPS_SSH_KEY_PATH, got %+v", missingRes.Error.Details)
	}

	// 2. Flag injection input rejected before worker spawn
	t.Setenv("VPS_SSH_KEY_PATH", secretKeyPath)
	badInputRes := sup.ExecuteBundle(context.Background(), manifest, src, workDir, map[string]any{
		"host": "-oProxyCommand=id",
		"user": "ops",
	}, 0, contract.ScopeGlobal)
	if badInputRes.OK || badInputRes.Error == nil || badInputRes.Error.Code != contract.ErrInvalidInput {
		t.Fatalf("expected INVALID_INPUT for flag injection, got ok=%v err=%+v", badInputRes.OK, badInputRes.Error)
	}

	// 3. Degraded health check succeeds with schema-valid structured output
	okRes := sup.ExecuteBundle(context.Background(), manifest, src, workDir, map[string]any{
		"host":          "198.51.100.10",
		"user":          "deploy",
		"services":      []string{"nginx", "postgresql"},
		"disk_warn_pct": 80,
	}, 0, contract.ScopeGlobal)
	if !okRes.OK {
		t.Fatalf("vps-health failed: %+v", okRes.Error)
	}
	if healthy, _ := okRes.Output["healthy"].(bool); healthy {
		t.Fatalf("expected healthy=false due to disk and inactive postgresql")
	}
	if okRes.Output["load_1m"] != 1.25 || okRes.Output["memory_used_pct"] != float64(50) || okRes.Output["disk_used_pct"] != float64(88) {
		t.Fatalf("unexpected parsed metrics: %+v", okRes.Output)
	}
	if okRes.Output["http_check"] != nil {
		t.Fatalf("expected http_check=nil, got %+v", okRes.Output["http_check"])
	}
	if services, _ := okRes.Output["services"].([]any); len(services) != 2 {
		t.Fatalf("expected 2 services, got %+v", services)
	}
	if warnings, _ := okRes.Output["warnings"].([]any); len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %+v", warnings)
	}

	// 4. Secret value echoed in child stderr is redacted in returned error
	leakRes := sup.ExecuteBundle(context.Background(), manifest, src, workDir, map[string]any{
		"host": "fail-leak.example.com",
		"user": "deploy",
	}, 0, contract.ScopeGlobal)
	if leakRes.OK || leakRes.Error == nil || leakRes.Error.Code != contract.ErrToolError {
		t.Fatalf("expected TOOL_ERROR, got ok=%v err=%+v", leakRes.OK, leakRes.Error)
	}
	if strings.Contains(leakRes.Error.Message, secretKeyPath) {
		t.Fatalf("secret leaked in error message: %q", leakRes.Error.Message)
	}
	if !strings.Contains(leakRes.Error.Message, "[REDACTED:VPS_SSH_KEY_PATH]") {
		t.Fatalf("expected [REDACTED:VPS_SSH_KEY_PATH] in error message, got %q", leakRes.Error.Message)
	}
}
