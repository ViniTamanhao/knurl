package host_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/host"
)

func assertErrCode(t *testing.T, err error, want contract.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s, got nil", want)
	}
	var toolErr *contract.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *contract.ToolError, got %T: %v", err, err)
	}
	if toolErr.Code != want {
		t.Fatalf("error code = %s, want %s (msg=%q)", toolErr.Code, want, toolErr.Message)
	}
}

func writeTestFile(t *testing.T, rootDir, rel, content string) {
	t.Helper()
	full := filepath.Join(rootDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", full, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
}

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func TestFSRootConfinementAndOperations(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()
	writeTestFile(t, outsideDir, "secret.txt", "top-secret-outside")

	writeTestFile(t, workDir, "docs/guide.md", "# Guide\nhello")
	writeTestFile(t, workDir, "docs/utf8.txt", "aéîô") // multi-byte runes
	writeTestFile(t, workDir, "secret.txt", "top-secret-inside")
	writeTestFile(t, workDir, ".hidden/note.md", "hidden note")
	writeTestFile(t, workDir, "ignored/skip.md", "ignored")

	// Symlink inside docs/ pointing to secret.txt outside docs/** allowlist
	if err := os.Symlink("../secret.txt", filepath.Join(workDir, "docs", "intra-leak.md")); err != nil {
		t.Fatalf("symlink intra-leak: %v", err)
	}
	// Symlink inside docs/ escaping outside workspace root
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(workDir, "docs", "abs-escape.md")); err != nil {
		t.Fatalf("symlink abs-escape: %v", err)
	}
	// Relative symlink escaping workspace root via ..
	if err := os.Symlink("../../outside.txt", filepath.Join(workDir, "docs", "rel-escape.md")); err != nil {
		t.Fatalf("symlink rel-escape: %v", err)
	}
	// Valid symlink within docs/**
	if err := os.Symlink("guide.md", filepath.Join(workDir, "docs", "alias.md")); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}

	manifest := contract.Manifest{
		SchemaVersion: "1",
		Name:          "fs-test",
		Version:       1,
		Description:   "Filesystem confinement test manifest",
		ReadOnly:      true,
		Capabilities: contract.Capabilities{
			FS: &contract.FSCapability{
				Read: []string{"docs/**"},
			},
		},
	}

	h, err := host.New(manifest, workDir, nil, nil)
	if err != nil {
		t.Fatalf("host.New: %v", err)
	}
	defer func() { _ = h.Close() }()

	// Allowed read
	content, err := h.FSRead("docs/guide.md", host.FSReadOptions{})
	if err != nil {
		t.Fatalf("FSRead(docs/guide.md): %v", err)
	}
	if content != "# Guide\nhello" {
		t.Fatalf("unexpected content: %q", content)
	}

	// Allowed intra-policy symlink read
	aliasContent, err := h.FSRead("docs/alias.md", host.FSReadOptions{})
	if err != nil {
		t.Fatalf("FSRead(docs/alias.md): %v", err)
	}
	if aliasContent != "# Guide\nhello" {
		t.Fatalf("unexpected alias content: %q", aliasContent)
	}

	// Blocked paths and symlink escapes
	for _, bad := range []string{
		"../secret.txt",
		"/etc/passwd",
		"docs/../secret.txt",
		"secret.txt",
		"docs/intra-leak.md",
		"docs/abs-escape.md",
		"docs/rel-escape.md",
	} {
		_, readErr := h.FSRead(bad, host.FSReadOptions{})
		assertErrCode(t, readErr, contract.ErrCapabilityDenied)

		_, statErr := h.FSStat(bad)
		assertErrCode(t, statErr, contract.ErrCapabilityDenied)
	}

	// Allowed missing file -> NOT_FOUND for read, exists:false for stat
	_, notFoundErr := h.FSRead("docs/missing.md", host.FSReadOptions{})
	assertErrCode(t, notFoundErr, contract.ErrNotFound)

	missingStat, statMissingErr := h.FSStat("docs/missing.md")
	if statMissingErr != nil {
		t.Fatalf("FSStat(docs/missing.md): %v", statMissingErr)
	}
	if missingStat.Exists || missingStat.IsDir || missingStat.Size != 0 || missingStat.ModTime != "" {
		t.Fatalf("unexpected missing stat: %+v", missingStat)
	}

	presentStat, statPresentErr := h.FSStat("docs/guide.md")
	if statPresentErr != nil {
		t.Fatalf("FSStat(docs/guide.md): %v", statPresentErr)
	}
	if !presentStat.Exists || presentStat.IsDir || presentStat.Size == 0 || presentStat.ModTime == "" {
		t.Fatalf("unexpected present stat: %+v", presentStat)
	}

	// Byte cap enforcement and UTF-8 safe truncation
	_, limitErr := h.FSRead("docs/guide.md", host.FSReadOptions{MaxBytes: 4, HasMax: true, Truncate: false})
	assertErrCode(t, limitErr, contract.ErrLimitExceeded)

	trunc, truncErr := h.FSRead("docs/utf8.txt", host.FSReadOptions{MaxBytes: 2, HasMax: true, Truncate: true})
	if truncErr != nil {
		t.Fatalf("FSRead truncate: %v", truncErr)
	}
	if trunc != "a" {
		t.Fatalf("truncated UTF-8 = %q, want %q", trunc, "a")
	}

	// Glob respects policy, skips escaping symlinks, respects ignore and max_results
	globbed, globErr := h.FSGlob("**", host.FSGlobOptions{
		Ignore: []string{"docs/utf8.txt"},
	})
	if globErr != nil {
		t.Fatalf("FSGlob: %v", globErr)
	}
	wantGlob := []string{"docs/alias.md", "docs/guide.md"}
	if !slices.Equal(globbed, wantGlob) {
		t.Fatalf("FSGlob = %v, want %v", globbed, wantGlob)
	}

	limitedGlob, limitedErr := h.FSGlob("**", host.FSGlobOptions{MaxResults: 1, HasMax: true})
	if limitedErr != nil {
		t.Fatalf("FSGlob max_results: %v", limitedErr)
	}
	if len(limitedGlob) != 1 {
		t.Fatalf("FSGlob max_results=1 len=%d, want 1", len(limitedGlob))
	}

	// IncludeHidden picks up .hidden files when policy allows "**"
	allManifest := manifest
	allManifest.Capabilities.FS = &contract.FSCapability{Read: []string{"**"}}
	hAll, err := host.New(allManifest, workDir, nil, nil)
	if err != nil {
		t.Fatalf("host.New all: %v", err)
	}
	defer func() { _ = hAll.Close() }()
	hiddenGlob, hiddenErr := hAll.FSGlob("**", host.FSGlobOptions{
		IncludeHidden: true,
		Ignore:        []string{"ignored/**"},
	})
	if hiddenErr != nil {
		t.Fatalf("FSGlob include_hidden: %v", hiddenErr)
	}
	if !slices.Contains(hiddenGlob, ".hidden/note.md") || slices.Contains(hiddenGlob, "ignored/skip.md") {
		t.Fatalf("unexpected hidden/ignored glob result: %v", hiddenGlob)
	}
}

func TestSecretsAndRedaction(t *testing.T) {
	resolved, err := host.ResolveSecrets([]contract.SecretRef{
		{Name: "REQ_KEY", Required: true, Description: "required"},
		{Name: "OPT_KEY", Required: false, Description: "optional"},
	}, func(name string) (string, bool) {
		if name == "REQ_KEY" {
			return "super_secret_value+1/2=", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("ResolveSecrets(present): %v", err)
	}
	if resolved["REQ_KEY"] != "super_secret_value+1/2=" {
		t.Fatalf("unexpected resolved secret: %v", resolved)
	}
	if _, ok := resolved["OPT_KEY"]; ok {
		t.Fatalf("expected optional unset secret to be omitted")
	}

	_, missingErr := host.ResolveSecrets([]contract.SecretRef{
		{Name: "MISSING_KEY", Required: true, Description: "required"},
	}, func(string) (string, bool) { return "", false })
	assertErrCode(t, missingErr, contract.ErrSecretMissing)

	secretVal := "super_secret_value+1/2="
	redactor := host.NewRedactor(map[string]string{"API_TOKEN": secretVal})
	input := strings.Join([]string{
		"raw=" + secretVal,
		"b64=" + base64.StdEncoding.EncodeToString([]byte(secretVal)),
		"rawurl=" + base64.RawURLEncoding.EncodeToString([]byte(secretVal)),
		"q=" + url.QueryEscape(secretVal),
	}, " ")
	scrubbed := redactor.String(input)
	if strings.Contains(scrubbed, secretVal) || strings.Contains(scrubbed, "c3VwZXJfc2VjcmV0") {
		t.Fatalf("secret leaked after redaction: %q", scrubbed)
	}
	if strings.Count(scrubbed, "[REDACTED:API_TOKEN]") != 4 {
		t.Fatalf("expected 4 redaction placeholders, got %q", scrubbed)
	}
}

func TestExecTemplatesAndTimeout(t *testing.T) {
	workDir := t.TempDir()
	secretVal := "s3cr3t-token-value"
	secrets := map[string]string{"MY_TOKEN": secretVal}

	manifest := contract.Manifest{
		SchemaVersion: "1",
		Name:          "exec-test",
		Version:       1,
		Description:   "Exec template enforcement test",
		ReadOnly:      true,
		Secrets: []contract.SecretRef{
			{Name: "MY_TOKEN", Required: true, Description: "Test secret"},
		},
		Capabilities: contract.Capabilities{
			Exec: &contract.ExecCapability{
				Templates: []contract.ExecTemplate{
					{
						ID:          "echo-arg",
						Program:     "sh", // intentionally forbidden to test defense-in-depth
						Args:        []string{"-c", "echo hi"},
						ReadOnly:    true,
						Description: "Forbidden shell check",
					},
					{
						ID:      "print-args",
						Program: "python3",
						Args:    []string{"-c", "import os, sys; print('|'.join(sys.argv[1:])); print('ENV_HAS_SECRET=' + str('MY_TOKEN' in os.environ)); print('ENV_EXTRA=' + os.environ.get('PASS_IN','')); sys.stderr.write('err:' + '${secret:MY_TOKEN}'); sys.exit(int('${arg:code}'))"},
						ArgsSchema: map[string]contract.ExecArgSchema{
							"name": {Pattern: `^[a-zA-Z0-9_${}:-]+$`, MaxLength: 64},
							"code": {Pattern: `^[0-9]+$`, MaxLength: 2},
						},
						Env: map[string]string{
							"PASS_IN": "${secret:MY_TOKEN}",
						},
						ReadOnly:    true,
						Description: "Wait/echo test",
					},
					{
						ID:          "sleep-child",
						Program:     "sleep",
						Args:        []string{"30"},
						ReadOnly:    true,
						Description: "Slow sleep command",
					},
				},
			},
		},
	}

	// Better: use `env` / `printf` / `echo` without python dependency! Let's replace python3 template with `env` and `printf`/`sh`-free standard POSIX utilities (`printenv` / `echo`).
	// Wait: test helper binary? No, let's compile or use `echo` and `sleep` and a small shell-script fixture? Wait: shell scripts with `#!/bin/sh` shebang executed directly as `program` (not `sh`) or a compiled Go test helper! Actually in `TestExecTemplatesAndTimeout`, we can write a tiny Go program or POSIX `echo`/`sleep`/`env`? Wait: `printenv` / `env` / `echo` exist on Linux, and for non-zero exit + stderr + args inspection, `go build` or `os.Executable()` self-reexec helper is 100% portable and zero-shell! Let's check: a self-reexec test helper or `echo`/`false`/`sleep` works everywhere!
	_ = manifest
	_ = workDir

	// Build a clean manifest using POSIX `echo`, `false`, `sleep`, `env` — wait, let's check if `echo` and `false` and `env` work cleanly, AND for custom stderr/exit/env checks we can use a tiny script in `t.TempDir()` or Go binary!
	helperBin := filepath.Join(t.TempDir(), "knurl-exec-helper")
	helperSrc := filepath.Join(t.TempDir(), "helper.go")
	helperCode := `package main
import (
	"fmt"
	"os"
	"strings"
	"time"
)
func main() {
	mode := os.Args[1]
	switch mode {
	case "inspect":
		fmt.Printf("args=%s|env_secret=%v|env_parent=%v|pass=%s\n",
			strings.Join(os.Args[2:], ","),
			os.Getenv("MY_TOKEN") != "",
			os.Getenv("PARENT_LEAK") != "",
			os.Getenv("PASS_IN"))
		fmt.Fprintln(os.Stderr, "stderr-secret="+os.Getenv("PASS_IN"))
	case "nonzero":
		fmt.Fprintln(os.Stdout, "out-before-exit")
		fmt.Fprintln(os.Stderr, "err-secret="+os.Getenv("PASS_IN"))
		os.Exit(7)
	case "big":
		fmt.Print(strings.Repeat("A", 300000))
		fmt.Fprint(os.Stderr, strings.Repeat("B", 300000))
	case "sleep":
		time.Sleep(10 * time.Second)
	}
}
`
	if err := os.WriteFile(helperSrc, []byte(helperCode), 0o600); err != nil {
		t.Fatalf("write helper src: %v", err)
	}
	buildCmd := exec.Command("go", "build", "-o", helperBin, helperSrc)
	if out, buildErr := buildCmd.CombinedOutput(); buildErr != nil {
		t.Fatalf("build helper: %v\n%s", buildErr, out)
	}
	t.Setenv("PATH", filepath.Dir(helperBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PARENT_LEAK", "should-not-leak")
	t.Setenv("MY_TOKEN", secretVal)

	m := contract.Manifest{
		SchemaVersion: "1",
		Name:          "exec-test",
		Version:       1,
		Description:   "Exec template enforcement test",
		ReadOnly:      true,
		Secrets: []contract.SecretRef{
			{Name: "MY_TOKEN", Required: true, Description: "Test secret"},
		},
		Capabilities: contract.Capabilities{
			Exec: &contract.ExecCapability{
				Templates: []contract.ExecTemplate{
					{
						ID:          "forbidden-sh",
						Program:     "sh",
						Args:        []string{"-c", "echo hi"},
						ReadOnly:    true,
						Description: "Forbidden shell",
					},
					{
						ID:      "inspect",
						Program: "knurl-exec-helper",
						Args:    []string{"inspect", "user=${arg:user}", "${arg:user}@${arg:host}"},
						ArgsSchema: map[string]contract.ExecArgSchema{
							"user": {Pattern: `^[a-zA-Z0-9_${}:-]+$`, MaxLength: 32},
							"host": {Pattern: `^[a-z0-9.-]+$`, MaxLength: 64},
						},
						Env: map[string]string{
							"PASS_IN": "${secret:MY_TOKEN}",
						},
						ReadOnly:    true,
						Description: "Inspect args and env",
					},
					{
						ID:      "nonzero",
						Program: "knurl-exec-helper",
						Args:    []string{"nonzero"},
						Env: map[string]string{
							"PASS_IN": "${secret:MY_TOKEN}",
						},
						ReadOnly:    true,
						Description: "Exit non-zero with secret on stderr",
					},
					{
						ID:          "big",
						Program:     "knurl-exec-helper",
						Args:        []string{"big"},
						ReadOnly:    true,
						Description: "Emit oversized streams",
					},
					{
						ID:          "slow",
						Program:     "knurl-exec-helper",
						Args:        []string{"sleep"},
						ReadOnly:    true,
						Description: "Sleep until interrupted",
					},
				},
			},
		},
	}

	h, err := host.New(m, workDir, secrets, host.NewRedactor(secrets))
	if err != nil {
		t.Fatalf("host.New: %v", err)
	}
	defer func() { _ = h.Close() }()

	// Undeclared template
	_, err = h.Exec(context.Background(), "unknown", nil, host.ExecOptions{})
	assertErrCode(t, err, contract.ErrCapabilityDenied)

	// Forbidden shell
	_, err = h.Exec(context.Background(), "forbidden-sh", nil, host.ExecOptions{})
	assertErrCode(t, err, contract.ErrCapabilityDenied)

	// Undeclared secret via SecretsHas
	_, err = h.SecretsHas("OTHER_SECRET")
	assertErrCode(t, err, contract.ErrCapabilityDenied)

	hasToken, err := h.SecretsHas("MY_TOKEN")
	if err != nil || !hasToken {
		t.Fatalf("SecretsHas(MY_TOKEN) = %v, %v; want true, nil", hasToken, err)
	}

	// Unknown param rejected
	_, err = h.Exec(context.Background(), "inspect", map[string]string{"user": "ops", "host": "a.com", "extra": "1"}, host.ExecOptions{})
	assertErrCode(t, err, contract.ErrInvalidArgument)

	// Invalid arg pattern rejected
	_, err = h.Exec(context.Background(), "inspect", map[string]string{"user": "ops", "host": "-oProxyCommand"}, host.ExecOptions{})
	assertErrCode(t, err, contract.ErrInvalidArgument)

	// Single-pass expansion + sanitized env + secret redaction
	res, err := h.Exec(context.Background(), "inspect", map[string]string{
		"user": "${secret:MY_TOKEN}",
		"host": "prod.example.com",
	}, host.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec(inspect): %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
	}
	// Passing literal ${secret:MY_TOKEN} as arg must NOT expand!
	if !strings.Contains(res.Stdout, "args=user=${secret:MY_TOKEN},${secret:MY_TOKEN}@prod.example.com") {
		t.Fatalf("expected literal arg without second-pass expansion, got %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "env_secret=false|env_parent=false|pass=[REDACTED:MY_TOKEN]") {
		t.Fatalf("expected sanitized env and redacted secret, got %q", res.Stdout)
	}
	if strings.Contains(res.Stdout, secretVal) || strings.Contains(res.Stderr, secretVal) {
		t.Fatalf("secret leaked in stdout/stderr: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "stderr-secret=[REDACTED:MY_TOKEN]") {
		t.Fatalf("expected redacted stderr, got %q", res.Stderr)
	}

	// Non-zero exit code returns normally and scrubs stderr
	nzRes, err := h.Exec(context.Background(), "nonzero", nil, host.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec(nonzero) unexpected err: %v", err)
	}
	if nzRes.ExitCode != 7 {
		t.Fatalf("nonzero ExitCode = %d, want 7", nzRes.ExitCode)
	}
	if strings.Contains(nzRes.Stderr, secretVal) || !strings.Contains(nzRes.Stderr, "[REDACTED:MY_TOKEN]") {
		t.Fatalf("unexpected nonzero stderr: %q", nzRes.Stderr)
	}

	// Byte caps
	bigRes, err := h.Exec(context.Background(), "big", nil, host.ExecOptions{})
	if err != nil {
		t.Fatalf("Exec(big): %v", err)
	}
	if !bigRes.Truncated || len(bigRes.Stdout) != host.MaxExecStreamBytes || len(bigRes.Stderr) != host.MaxExecStreamBytes {
		t.Fatalf("expected 256 KiB truncated streams, got len(out)=%d len(err)=%d truncated=%v", len(bigRes.Stdout), len(bigRes.Stderr), bigRes.Truncated)
	}

	// Timeout returns TIMEOUT promptly
	start := time.Now()
	_, timeoutErr := h.Exec(context.Background(), "slow", nil, host.ExecOptions{TimeoutMs: 100, HasTimeout: true})
	assertErrCode(t, timeoutErr, contract.ErrTimeout)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("exec timeout took too long (%v)", elapsed)
	}
}

func TestGitPrimitives(t *testing.T) {
	ctx := context.Background()
	nonGitDir := t.TempDir()
	gitManifest := contract.Manifest{
		SchemaVersion: "1",
		Name:          "git-test",
		Version:       1,
		Description:   "Read-only git capability test",
		ReadOnly:      true,
		Capabilities: contract.Capabilities{
			Git: &contract.GitCapability{Read: true},
		},
	}

	nonGitHost, err := host.New(gitManifest, nonGitDir, nil, nil)
	if err != nil {
		t.Fatalf("host.New non-git: %v", err)
	}
	defer func() { _ = nonGitHost.Close() }()
	_, notRepoErr := nonGitHost.GitBranch(ctx)
	assertErrCode(t, notRepoErr, contract.ErrNotAGitRepo)

	repoDir := t.TempDir()
	runGitTest(t, repoDir, "init", "-b", "main")
	runGitTest(t, repoDir, "config", "user.name", "Test User")
	runGitTest(t, repoDir, "config", "user.email", "test@example.com")
	runGitTest(t, repoDir, "config", "commit.gpgsign", "false")

	gitHost, err := host.New(gitManifest, repoDir, nil, nil)
	if err != nil {
		t.Fatalf("host.New repo: %v", err)
	}
	defer func() { _ = gitHost.Close() }()

	// Unborn HEAD should still report branch and empty log cleanly
	unbornBranch, err := gitHost.GitBranch(ctx)
	if err != nil {
		t.Fatalf("GitBranch unborn: %v", err)
	}
	if unbornBranch.Current != "main" || unbornBranch.Detached || unbornBranch.Head != "" {
		t.Fatalf("unexpected unborn branch: %+v", unbornBranch)
	}
	unbornLog, err := gitHost.GitLog(ctx, host.GitLogOptions{})
	if err != nil || len(unbornLog) != 0 {
		t.Fatalf("GitLog unborn = %v, %v; want empty slice", unbornLog, err)
	}

	writeTestFile(t, repoDir, "main.go", "package main\nfunc main() {}\n")
	writeTestFile(t, repoDir, "README.md", "# Hello\n")
	runGitTest(t, repoDir, "add", "main.go", "README.md")
	runGitTest(t, repoDir, "commit", "-m", "initial commit")

	// Modify unstaged, add staged, add untracked
	writeTestFile(t, repoDir, "main.go", "package main\n// updated\nfunc main() {}\n")
	writeTestFile(t, repoDir, "staged.txt", "staged\n")
	runGitTest(t, repoDir, "add", "staged.txt")
	writeTestFile(t, repoDir, "untracked.txt", "untracked\n")

	br, err := gitHost.GitBranch(ctx)
	if err != nil {
		t.Fatalf("GitBranch: %v", err)
	}
	if br.Current != "main" || br.Detached || len(br.Head) < 7 {
		t.Fatalf("unexpected branch: %+v", br)
	}

	st, err := gitHost.GitStatus(ctx)
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}
	if st.Clean || len(st.Staged) != 1 || len(st.Unstaged) != 1 || len(st.Untracked) != 1 {
		t.Fatalf("unexpected status: %+v", st)
	}

	commits, err := gitHost.GitLog(ctx, host.GitLogOptions{Limit: 5, HasLimit: true})
	if err != nil {
		t.Fatalf("GitLog: %v", err)
	}
	if len(commits) != 1 || commits[0].Subject != "initial commit" {
		t.Fatalf("unexpected commits: %+v", commits)
	}

	statDiff, err := gitHost.GitDiff(ctx, host.GitDiffOptions{StatOnly: true})
	if err != nil {
		t.Fatalf("GitDiff stat_only: %v", err)
	}
	if statDiff.Patch != "" || len(statDiff.Files) != 1 || statDiff.Files[0].Path != "main.go" {
		t.Fatalf("unexpected statOnly diff: %+v", statDiff)
	}

	fullDiff, err := gitHost.GitDiff(ctx, host.GitDiffOptions{Staged: true})
	if err != nil {
		t.Fatalf("GitDiff staged: %v", err)
	}
	if !strings.Contains(fullDiff.Patch, "staged.txt") || len(fullDiff.Files) != 1 {
		t.Fatalf("unexpected staged diff: %+v", fullDiff)
	}

	// Flag injection in ref rejected
	_, badRefErr := gitHost.GitLog(ctx, host.GitLogOptions{Ref: "--exec=id"})
	assertErrCode(t, badRefErr, contract.ErrInvalidArgument)

	// Detached HEAD state
	runGitTest(t, repoDir, "checkout", "--detach", "HEAD")
	detachedBr, err := gitHost.GitBranch(ctx)
	if err != nil {
		t.Fatalf("GitBranch detached: %v", err)
	}
	if !detachedBr.Detached || detachedBr.Current != "" || detachedBr.Head == "" {
		t.Fatalf("unexpected detached branch: %+v", detachedBr)
	}
}
