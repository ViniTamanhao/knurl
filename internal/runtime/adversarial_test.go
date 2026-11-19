package runtime_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/host"
	"github.com/ViniTamanhao/knurl/internal/registry"
	"github.com/ViniTamanhao/knurl/internal/runtime"
)

type advRoutingTransport struct {
	routes map[string]*httptest.Server
}

func (rt *advRoutingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	srv, ok := rt.routes[strings.ToLower(req.URL.Hostname())]
	if !ok {
		return nil, fmt.Errorf("no test route for host %q", req.URL.Hostname())
	}
	targetURL, err := url.Parse(srv.URL)
	if err != nil {
		return nil, err
	}
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = targetURL.Scheme
	cloned.URL.Host = targetURL.Host
	cloned.Host = req.URL.Host
	return srv.Client().Transport.RoundTrip(cloned)
}

func TestAdversarialCPUInfiniteLoopAndNoOrphanChildren(t *testing.T) {
	sup := &runtime.Supervisor{}
	m := baseTestManifest()

	// 1. Infinite JS loop killed promptly within timeout.
	start := time.Now()
	res := sup.ExecuteBundle(context.Background(), m, `function main() { while (true) {} }`, t.TempDir(), nil, 150, contract.ScopeRepo)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrTimeout {
		t.Fatalf("expected TIMEOUT on infinite loop, got ok=%v err=%+v", res.OK, res.Error)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("infinite loop took too long to terminate: %v", elapsed)
	}

	// 2. Child process spawned by _worker via knurl.exec writes its PID and sleeps;
	// after worker timeout, zero orphan child processes remain.
	helperBin := filepath.Join(t.TempDir(), "orphan-probe")
	helperSrc := filepath.Join(t.TempDir(), "main.go")
	helperCode := `package main
import (
	"fmt"
	"os"
	"strconv"
	"time"
)
func main() {
	if err := os.WriteFile("child.pid", []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(2)
	}
	fmt.Println("ready")
	time.Sleep(30 * time.Second)
}
`
	if err := os.WriteFile(helperSrc, []byte(helperCode), 0o600); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", helperBin, helperSrc).CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	t.Setenv("PATH", filepath.Dir(helperBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	workDir := t.TempDir()
	execManifest := baseTestManifest()
	execManifest.Capabilities.Exec = &contract.ExecCapability{
		Templates: []contract.ExecTemplate{
			{
				ID:          "spawn-sleeper",
				Program:     "orphan-probe",
				Args:        []string{"run"},
				ReadOnly:    true,
				Description: "Spawn sleeper to verify process-group cleanup",
			},
		},
	}

	res = sup.ExecuteBundle(
		context.Background(),
		execManifest,
		`function main() { knurl.exec("spawn-sleeper"); return { greeting: "done" }; }`,
		workDir,
		nil,
		200,
		contract.ScopeRepo,
	)
	if res.OK || res.Error == nil || res.Error.Code != contract.ErrTimeout {
		t.Fatalf("expected TIMEOUT on sleeper child, got ok=%v err=%+v", res.OK, res.Error)
	}

	pidBytes, err := os.ReadFile(filepath.Join(workDir, "child.pid"))
	if err != nil {
		t.Fatalf("expected child.pid written before timeout: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || childPID <= 0 {
		t.Fatalf("invalid child PID %q: %v", string(pidBytes), err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for isPIDAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if isPIDAlive(childPID) {
		t.Fatalf("orphan child process PID %d survived worker timeout", childPID)
	}

	// Parent must remain healthy after worker timeout.
	healthy := sup.ExecuteBundle(context.Background(), m, `function main() { return { greeting: "ok" }; }`, workDir, nil, 0, contract.ScopeRepo)
	if !healthy.OK || healthy.Output["greeting"] != "ok" {
		t.Fatalf("parent unhealthy after timeout: %+v", healthy)
	}
}

func TestAdversarialMemoryAndStackBombs(t *testing.T) {
	sup := &runtime.Supervisor{}
	m := baseTestManifest()
	workDir := t.TempDir()

	// 1. Exponential string concatenation bomb fails safely without crashing parent.
	strRes := sup.ExecuteBundle(
		context.Background(),
		m,
		`function main() { var s = "x"; while (true) { s += s; } }`,
		workDir,
		nil,
		350,
		contract.ScopeRepo,
	)
	if strRes.OK || strRes.Error == nil {
		t.Fatalf("expected failure on exponential string bomb, got %+v", strRes)
	}
	switch strRes.Error.Code {
	case contract.ErrLimitExceeded, contract.ErrTimeout, contract.ErrToolError:
	default:
		t.Fatalf("unexpected error code for string bomb: %+v", strRes.Error)
	}

	// 2. Real heap array allocation bomb hits _worker heap watchdog cleanly before timeout.
	arrStart := time.Now()
	arrRes := sup.ExecuteBundle(
		context.Background(),
		m,
		`function main() {
  var chunk = "x".repeat(1024 * 1024);
  var arr = [];
  while (true) {
    arr.push(chunk + String(arr.length));
  }
}`,
		workDir,
		nil,
		8000,
		contract.ScopeRepo,
	)
	if arrRes.OK || arrRes.Error == nil || arrRes.Error.Code != contract.ErrLimitExceeded {
		t.Fatalf("expected LIMIT_EXCEEDED on heap allocation bomb, got ok=%v err=%+v (elapsed=%v)", arrRes.OK, arrRes.Error, time.Since(arrStart))
	}

	// 3. Unbounded recursion caught by MaxVMCallStackSize.
	recRes := sup.ExecuteBundle(
		context.Background(),
		m,
		`function boom(n) { return boom(n + 1); } function main() { return { greeting: String(boom(0)) }; }`,
		workDir,
		nil,
		2000,
		contract.ScopeRepo,
	)
	if recRes.OK || recRes.Error == nil || recRes.Error.Code != contract.ErrToolError {
		t.Fatalf("expected TOOL_ERROR on stack overflow, got ok=%v err=%+v", recRes.OK, recRes.Error)
	}

	// 4. Parent remains responsive and executes valid tools cleanly.
	okRes := sup.ExecuteBundle(context.Background(), m, `function main() { return { greeting: "healthy-after-bombs" }; }`, workDir, nil, 0, contract.ScopeRepo)
	if !okRes.OK || okRes.Output["greeting"] != "healthy-after-bombs" {
		t.Fatalf("parent unhealthy after memory bombs: %+v", okRes)
	}
}

func TestAdversarialOutputAndIPCFlood(t *testing.T) {
	sup := &runtime.Supervisor{}
	m := baseTestManifest()
	workDir := t.TempDir()

	// 1. Massive JSON return payload trips 2 MiB IPC limit cleanly.
	overRes := sup.ExecuteBundle(
		context.Background(),
		m,
		`function main() { return { greeting: "A".repeat(3 * 1024 * 1024) }; }`,
		workDir,
		nil,
		0,
		contract.ScopeRepo,
	)
	if overRes.OK || overRes.Error == nil || overRes.Error.Code != contract.ErrLimitExceeded {
		t.Fatalf("expected LIMIT_EXCEEDED on >2 MiB output, got %+v", overRes)
	}

	// 2. Massive knurl.log flood is capped cleanly at <= 256 lines and <= 64 KiB.
	logRes := sup.ExecuteBundle(
		context.Background(),
		m,
		`function main() {
  var line = "L".repeat(2048);
  for (var i = 0; i < 400; i++) {
    knurl.log(line);
  }
  return { greeting: "logged" };
}`,
		workDir,
		nil,
		0,
		contract.ScopeRepo,
	)
	if !logRes.OK {
		t.Fatalf("expected bounded log flood tool to succeed, got %+v", logRes.Error)
	}
	if len(logRes.Diagnostics) > 256 {
		t.Fatalf("diagnostics exceeded 256 lines: %d", len(logRes.Diagnostics))
	}
	totalDiagBytes := 0
	for _, d := range logRes.Diagnostics {
		totalDiagBytes += len(d)
	}
	if totalDiagBytes > 64*1024 {
		t.Fatalf("diagnostics exceeded 64 KiB cap: %d", totalDiagBytes)
	}

	// 3. Massive exec stdout/stderr flood does not deadlock IPC pipes.
	floodBin := filepath.Join(t.TempDir(), "flood-helper")
	floodSrc := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(floodSrc, []byte(`package main
import (
	"fmt"
	"os"
	"strings"
)
func main() {
	fmt.Print(strings.Repeat("O", 512*1024))
	fmt.Fprint(os.Stderr, strings.Repeat("E", 512*1024))
}
`), 0o600); err != nil {
		t.Fatalf("write flood helper: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", floodBin, floodSrc).CombinedOutput(); err != nil {
		t.Fatalf("build flood helper: %v\n%s", err, out)
	}
	t.Setenv("PATH", filepath.Dir(floodBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	execManifest := baseTestManifest()
	execManifest.Capabilities.Exec = &contract.ExecCapability{
		Templates: []contract.ExecTemplate{
			{
				ID:          "flood",
				Program:     "flood-helper",
				Args:        []string{"run"},
				ReadOnly:    true,
				Description: "Emit 512 KiB to stdout and stderr",
			},
		},
	}
	execRes := sup.ExecuteBundle(
		context.Background(),
		execManifest,
		`function main() {
  var r = knurl.exec("flood");
  return { greeting: r.truncated + ":" + r.stdout.length + ":" + r.stderr.length };
}`,
		workDir,
		nil,
		0,
		contract.ScopeRepo,
	)
	if !execRes.OK || execRes.Output["greeting"] != "true:262144:262144" {
		t.Fatalf("unexpected exec flood result: ok=%v out=%+v err=%+v", execRes.OK, execRes.Output, execRes.Error)
	}
}

func TestAdversarialFilesystemEscapesVM(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("outside-secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "docs", "ok.md"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("write docs/ok.md: %v", err)
	}
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(workDir, "docs", "link-file.md")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(workDir, "docs", "link-dir")); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}

	m := baseTestManifest()
	m.Capabilities.FS = &contract.FSCapability{Read: []string{"docs/**"}}

	src := `
function expectDenied(fn) {
  try {
    fn();
    throw new Error("expected CAPABILITY_DENIED");
  } catch (e) {
    if (!e || e.code !== "CAPABILITY_DENIED") {
      throw new Error("wrong code: " + (e && e.code) + " msg=" + (e && e.message));
    }
    return 1;
  }
}
function main() {
  var denied = 0;
  denied += expectDenied(function() { knurl.fs.read("../secret.txt"); });
  denied += expectDenied(function() { knurl.fs.read("/etc/passwd"); });
  denied += expectDenied(function() { knurl.fs.read("docs/link-file.md"); });
  denied += expectDenied(function() { knurl.fs.stat("docs/link-file.md"); });
  denied += expectDenied(function() { knurl.fs.read("docs/link-dir/secret.txt"); });
  denied += expectDenied(function() { knurl.fs.stat("docs/link-dir/secret.txt"); });
  denied += expectDenied(function() { knurl.fs.glob("docs/link-dir/**"); });
  denied += expectDenied(function() { knurl.fs.glob("other/**"); });
  var visible = knurl.fs.glob("**");
  return { greeting: "denied=" + denied + "|visible=" + visible.join(",") };
}
`
	sup := &runtime.Supervisor{}
	res := sup.ExecuteBundle(context.Background(), m, src, workDir, nil, 0, contract.ScopeRepo)
	if !res.OK || res.Output["greeting"] != "denied=8|visible=docs/ok.md" {
		t.Fatalf("unexpected FS escape VM result: ok=%v out=%+v err=%+v", res.OK, res.Output, res.Error)
	}
}

func TestAdversarialExecAndGitInjectionVM(t *testing.T) {
	workDir := t.TempDir()
	runGitExampleTest(t, workDir, "init", "-b", "main")
	runGitExampleTest(t, workDir, "config", "user.name", "Test")
	runGitExampleTest(t, workDir, "config", "user.email", "test@example.com")
	runGitExampleTest(t, workDir, "config", "commit.gpgsign", "false")
	writeFileExampleTest(t, workDir, "README.md", "# Test\n")
	runGitExampleTest(t, workDir, "add", "README.md")
	runGitExampleTest(t, workDir, "commit", "-m", "init")

	m := baseTestManifest()
	m.Capabilities.Git = &contract.GitCapability{Read: true}
	m.Capabilities.Exec = &contract.ExecCapability{
		Templates: []contract.ExecTemplate{
			{
				ID:      "git-show-ref",
				Program: "git",
				Args:    []string{"rev-parse", "--verify", "${arg:ref}"},
				ArgsSchema: map[string]contract.ExecArgSchema{
					"ref": {Pattern: `^[a-zA-Z0-9._/-]+$`, MaxLength: 64},
				},
				ReadOnly:    true,
				Description: "Resolve safe git ref",
			},
			{
				ID:      "ssh-probe",
				Program: "ssh",
				Args:    []string{"-o", "BatchMode=yes", "${arg:host}", "uname"},
				ArgsSchema: map[string]contract.ExecArgSchema{
					"host": {Pattern: `^[a-zA-Z0-9][a-zA-Z0-9.-]*$`, MaxLength: 64},
				},
				ReadOnly:    true,
				Description: "Constrained ssh template",
			},
		},
	}

	src := `
function expectCode(code, fn) {
  try {
    fn();
    throw new Error("expected " + code);
  } catch (e) {
    if (!e || e.code !== code) {
      throw new Error("got " + (e && e.code) + " want " + code);
    }
    return 1;
  }
}
function main() {
  var n = 0;
  n += expectCode("CAPABILITY_DENIED", function() { knurl.exec("sh-unlisted", {}); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.exec("git-show-ref", { ref: "-c core.sshCommand=id" }); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.exec("ssh-probe", { host: "-o ProxyCommand=id" }); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.exec("git-show-ref", { ref: "main; rm -rf /" }); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.exec("git-show-ref", { ref: "main | cat" }); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.exec("git-show-ref", { ref: "$(whoami)" }); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.git.log({ ref: "--upload-pack=id" }); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.git.log({ path: "--output=/tmp/pwn" }); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.git.diff({ base: "-p" }); });
  n += expectCode("INVALID_ARGUMENT", function() { knurl.git.diff({ path: ":(glob)**" }); });
  return { greeting: "blocked=" + n };
}
`
	sup := &runtime.Supervisor{}
	res := sup.ExecuteBundle(context.Background(), m, src, workDir, nil, 0, contract.ScopeRepo)
	if !res.OK || res.Output["greeting"] != "blocked=10" {
		t.Fatalf("unexpected exec/git injection VM result: ok=%v out=%+v err=%+v", res.OK, res.Output, res.Error)
	}
}

func TestAdversarialHTTPDomainAndRedirectBypassVM(t *testing.T) {
	m := baseTestManifest()
	m.Capabilities.HTTP = &contract.HTTPCapability{
		Allow: []contract.HTTPRule{
			{Domain: "allowed.example.test", Methods: []string{"GET"}},
		},
	}

	// 1. Subprocess _worker blocks undeclared host and SSRF targets cleanly.
	sup := &runtime.Supervisor{}
	ssrfSrc := `
function expectCode(code, fn) {
  try {
    fn();
    throw new Error("expected " + code);
  } catch (e) {
    if (!e || e.code !== code) {
      throw new Error("got " + (e && e.code) + " want " + code + " msg=" + (e && e.message));
    }
    return 1;
  }
}
function main() {
  var n = 0;
  n += expectCode("CAPABILITY_DENIED", function() { knurl.http.get("https://evil.example.test/steal"); });
  n += expectCode("HTTP_ERROR", function() { knurl.http.get("https://169.254.169.254/latest/meta-data/"); });
  n += expectCode("HTTP_ERROR", function() { knurl.http.get("http://localhost:8080/"); });
  return { greeting: "http_blocked=" + n };
}
`
	ssrfRes := sup.ExecuteBundle(context.Background(), m, ssrfSrc, t.TempDir(), nil, 0, contract.ScopeRepo)
	if !ssrfRes.OK || ssrfRes.Output["greeting"] != "http_blocked=3" {
		t.Fatalf("unexpected HTTP SSRF VM result: ok=%v out=%+v err=%+v", ssrfRes.OK, ssrfRes.Output, ssrfRes.Error)
	}

	// 2. Allowlisted TLS httptest.Server returning 302 redirect to disallowed host fails closed with zero hits on evilSrv.
	var evilHits atomic.Int32
	evilSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		evilHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer evilSrv.Close()

	allowedSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, "https://evil.example.test/steal", http.StatusFound)
	}))
	defer allowedSrv.Close()

	rt := &advRoutingTransport{
		routes: map[string]*httptest.Server{
			"allowed.example.test": allowedSrv,
			"evil.example.test":    evilSrv,
		},
	}

	resp := runtime.ExecuteInProcessWithConfig(
		context.Background(),
		contract.WorkerRequest{
			Manifest:  m,
			Source:    `function main() { knurl.http.get("https://allowed.example.test/start"); return { greeting: "no" }; }`,
			WorkDir:   t.TempDir(),
			Input:     map[string]any{},
			TimeoutMs: 5000,
		},
		runtime.ProcessConfig{
			HostOptions: []host.Option{host.WithHTTPTransport(rt)},
		},
	)
	if resp.OK || resp.Error == nil || resp.Error.Code != contract.ErrHTTPError {
		t.Fatalf("expected HTTP_ERROR on 302 cross-domain redirect, got ok=%v err=%+v", resp.OK, resp.Error)
	}
	if evilHits.Load() != 0 {
		t.Fatalf("evilSrv reached (%d hits) on blocked redirect", evilHits.Load())
	}
}

func TestAdversarialSecretExfiltrationVM(t *testing.T) {
	const rawSecret = "top-secret-exfil-token-42"
	const multiSecret = "PEM_LINE_ONE_SECRET\nPEM_LINE_TWO_SECRET"

	// Helper binary that outputs received secret env var on stdout & stderr.
	helperDir := t.TempDir()
	helperBin := filepath.Join(helperDir, "echo-secret")
	helperSrc := filepath.Join(helperDir, "main.go")
	if err := os.WriteFile(helperSrc, []byte(`package main
import (
	"fmt"
	"os"
)
func main() {
	s := os.Getenv("INJECTED")
	fmt.Print("stdout=" + s)
	fmt.Fprint(os.Stderr, "stderr=" + s)
}
`), 0o600); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", helperBin, helperSrc).CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := contract.Manifest{
		SchemaVersion: "1",
		Name:          "exfil-test",
		Version:       1,
		Description:   "Secret exfiltration defense test",
		ReadOnly:      true,
		TimeoutMs:     5000,
		Secrets: []contract.SecretRef{
			{Name: "EXFIL_TOKEN", Required: true, Description: "Test single-line secret"},
			{Name: "PEM_SECRET", Required: true, Description: "Test multi-line secret"},
		},
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"mode": map[string]any{"type": "string"}},
		},
		OutputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		},
		Capabilities: contract.Capabilities{
			Exec: &contract.ExecCapability{
				Templates: []contract.ExecTemplate{
					{
						ID:          "echo-sec",
						Program:     "echo-secret",
						Args:        []string{"run"},
						Env:         map[string]string{"INJECTED": "${secret:EXFIL_TOKEN}"},
						ReadOnly:    true,
						Description: "Echo injected secret",
					},
				},
			},
			HTTP: &contract.HTTPCapability{
				Allow: []contract.HTTPRule{
					{
						Domain:  "echo.example.test",
						Methods: []string{"GET"},
						Secrets: []string{"EXFIL_TOKEN"},
					},
				},
			},
		},
	}

	assertNoSecretLeak := func(t *testing.T, v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		if !json.Valid(raw) {
			t.Fatalf("result is not valid JSON: %s", raw)
		}
		s := string(raw)
		for _, needle := range []string{
			rawSecret,
			base64.StdEncoding.EncodeToString([]byte(rawSecret)),
			url.QueryEscape(rawSecret),
			"PEM_LINE_ONE_SECRET",
			"PEM_LINE_TWO_SECRET",
		} {
			if strings.Contains(s, needle) {
				t.Fatalf("secret needle %q leaked in output: %s", needle, s)
			}
		}
	}

	sup := &runtime.Supervisor{
		EnvLookup: func(k string) (string, bool) {
			switch k {
			case "EXFIL_TOKEN":
				return rawSecret, true
			case "PEM_SECRET":
				return multiSecret, true
			default:
				return os.LookupEnv(k)
			}
		},
	}

	// 1. Exec stdout/stderr + return JSON field/key + knurl.log exfil over real _worker subprocess.
	srcReturn := `
function main() {
  var r = knurl.exec("echo-sec");
  knurl.log("diag=" + r.stdout);
  var out = {
    exec_out: r.stdout,
    exec_err: r.stderr,
    nested: [r.stdout, r.stderr]
  };
  return out;
}
`
	resExec := sup.ExecuteBundle(context.Background(), m, srcReturn, t.TempDir(), map[string]any{"mode": "exec"}, 0, contract.ScopeRepo)
	if !resExec.OK {
		t.Fatalf("exec exfil test failed: %+v", resExec.Error)
	}
	assertNoSecretLeak(t, resExec)

	// 2. Thrown JS exception exfil over real _worker subprocess.
	srcThrow := fmt.Sprintf(`function main() { throw new Error("boom %s %s"); }`,
		rawSecret, base64.StdEncoding.EncodeToString([]byte(rawSecret)))
	resThrow := sup.ExecuteBundle(context.Background(), m, srcThrow, t.TempDir(), map[string]any{"mode": "throw"}, 0, contract.ScopeRepo)
	if resThrow.OK || resThrow.Error == nil || resThrow.Error.Code != contract.ErrToolError {
		t.Fatalf("expected TOOL_ERROR on JS throw, got %+v", resThrow)
	}
	assertNoSecretLeak(t, resThrow)

	// 3. Multi-line secret leaked via output + throw via real _worker.
	srcMulti := `function main() { var r = knurl.exec("echo-sec"); return { leaked: r.stdout, multi: "PEM_LINE_ONE_SECRET\nPEM_LINE_TWO_SECRET" }; }`
	resMulti := sup.ExecuteBundle(context.Background(), m, srcMulti, t.TempDir(), map[string]any{"mode": "multi"}, 0, contract.ScopeRepo)
	if !resMulti.OK {
		t.Fatalf("multi exfil test failed: %+v", resMulti.Error)
	}
	assertNoSecretLeak(t, resMulti)

	// 4. HTTP response body + header echo exfil via ExecuteInProcessWithConfig + httptest.NewTLSServer.
	echoSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		w.Header().Set("X-Leaked-Auth", auth)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("echo=" + auth + " pem=PEM_LINE_ONE_SECRET"))
	}))
	defer echoSrv.Close()

	rt := &advRoutingTransport{routes: map[string]*httptest.Server{"echo.example.test": echoSrv}}
	httpResp := runtime.ExecuteInProcessWithConfig(
		context.Background(),
		contract.WorkerRequest{
			Manifest: m,
			Source: `function main() {
  var r = knurl.http.get("https://echo.example.test/echo", {
    headers: { Authorization: "Bearer ${secret:EXFIL_TOKEN}" }
  });
  return { body: r.body, hdr: r.headers["x-leaked-auth"] };
}`,
			WorkDir:   t.TempDir(),
			Input:     map[string]any{"mode": "http"},
			TimeoutMs: 5000,
			Secrets: map[string]string{
				"EXFIL_TOKEN": rawSecret,
				"PEM_SECRET":  multiSecret,
			},
		},
		runtime.ProcessConfig{HostOptions: []host.Option{host.WithHTTPTransport(rt)}},
	)
	if !httpResp.OK {
		t.Fatalf("HTTP echo test failed: %+v", httpResp.Error)
	}
	assertNoSecretLeak(t, httpResp)
}

func TestAdversarialSchemaTampering(t *testing.T) {
	m := baseTestManifest()

	// 1. Invalid input must be rejected BEFORE spawning _worker (proven by bogus Executable).
	noSpawnSup := &runtime.Supervisor{Executable: "/nonexistent/knurl-must-not-spawn"}
	badIn := noSpawnSup.ExecuteBundle(
		context.Background(),
		m,
		`function main() { return { greeting: "should not run" }; }`,
		t.TempDir(),
		map[string]any{"unexpected_prop": true},
		0,
		contract.ScopeRepo,
	)
	if badIn.OK || badIn.Error == nil || badIn.Error.Code != contract.ErrInvalidInput {
		t.Fatalf("expected pre-spawn INVALID_INPUT without spawning worker, got ok=%v err=%+v", badIn.OK, badIn.Error)
	}

	// 2. Schema-violating JS outputs rejected post-execution with INVALID_OUTPUT.
	sup := &runtime.Supervisor{}
	for _, badSrc := range []string{
		`function main() { return { wrong_key: "x" }; }`,
		`function main() { return { greeting: 123 }; }`,
		`function main() { return { greeting: "ok", extra: true }; }`,
		`function main() { return [1, 2]; }`,
	} {
		outRes := sup.ExecuteBundle(context.Background(), m, badSrc, t.TempDir(), map[string]any{}, 0, contract.ScopeRepo)
		if outRes.OK || outRes.Error == nil || outRes.Error.Code != contract.ErrInvalidOutput {
			t.Fatalf("expected INVALID_OUTPUT for %q, got ok=%v err=%+v", badSrc, outRes.OK, outRes.Error)
		}
	}
}

func TestAdversarialRegistryPrecedenceAndTraversal(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	workDir := filepath.Join(repoRoot, "pkg")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}

	writeTierTool := func(toolsDir, tier string) {
		t.Helper()
		verDir := filepath.Join(toolsDir, "shadow-tool", "v1")
		if err := os.MkdirAll(verDir, 0o755); err != nil {
			t.Fatalf("mkdir verDir: %v", err)
		}
		manifest := `{
  "schema_version": "1",
  "name": "shadow-tool",
  "version": 1,
  "description": "Shadowing precedence adversarial test",
  "read_only": true,
  "input_schema": {"type": "object", "additionalProperties": false},
  "output_schema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["greeting"],
    "properties": {"greeting": {"type": "string"}}
  },
  "capabilities": {}
}`
		if err := os.WriteFile(filepath.Join(verDir, "manifest.json"), []byte(manifest), 0o600); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		if err := os.WriteFile(filepath.Join(verDir, "tool.js"), []byte(fmt.Sprintf(`function main() { return { greeting: %q }; }`, tier)), 0o600); err != nil {
			t.Fatalf("write tool.js: %v", err)
		}
	}

	repoTools := filepath.Join(repoRoot, ".knurl", "tools")
	globalTools := filepath.Join(root, "global", "tools")
	writeTierTool(repoTools, "from-repo")
	writeTierTool(globalTools, "from-global")

	reg := &registry.Registry{WorkDir: workDir, GlobalDir: globalTools}
	b, err := reg.Resolve("shadow-tool", contract.ScopeAuto)
	if err != nil {
		t.Fatalf("Resolve(shadow-tool): %v", err)
	}
	sup := &runtime.Supervisor{}
	execRes := sup.ExecuteBundle(context.Background(), b.Manifest, b.Source, workDir, map[string]any{}, 0, b.Scope)
	if !execRes.OK || execRes.Scope != contract.ScopeRepo || execRes.Output["greeting"] != "from-repo" {
		t.Fatalf("expected repo shadow execution, got %+v", execRes)
	}

	// Traversal names fail closed with TOOL_NOT_FOUND.
	for _, evilName := range []string{"../../etc/passwd", "/etc/passwd", "shadow-tool/../other"} {
		_, travErr := reg.Resolve(evilName, contract.ScopeAuto)
		if travErr == nil || travErr.Error() == "" {
			t.Fatalf("expected error for traversal tool name %q", evilName)
		}
		if !strings.Contains(travErr.Error(), string(contract.ErrToolNotFound)) {
			t.Fatalf("expected TOOL_NOT_FOUND for %q, got %v", evilName, travErr)
		}
	}
}
