package host_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ViniTamanhao/knurl/internal/contract"
	"github.com/ViniTamanhao/knurl/internal/host"
)

type hostRoutingTransport struct {
	routes map[string]*httptest.Server
}

func (rt *hostRoutingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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

func TestSecurityFilesystemEscapes(t *testing.T) {
	workDir := t.TempDir()
	outsideDir := t.TempDir()
	writeTestFile(t, outsideDir, "secret.txt", "outside-secret-data")
	writeTestFile(t, workDir, "docs/allowed.md", "allowed content")
	writeTestFile(t, workDir, "private/secret.txt", "inside-private-secret")

	// Symlinks for escape vectors.
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(workDir, "docs", "ext-file.md")); err != nil {
		t.Fatalf("symlink ext-file: %v", err)
	}
	if err := os.Symlink("../../outside/secret.txt", filepath.Join(workDir, "docs", "rel-ext-file.md")); err != nil {
		t.Fatalf("symlink rel-ext-file: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(workDir, "docs", "ext-dir")); err != nil {
		t.Fatalf("symlink ext-dir: %v", err)
	}
	if err := os.Symlink("../private", filepath.Join(workDir, "docs", "priv-dir")); err != nil {
		t.Fatalf("symlink priv-dir: %v", err)
	}
	if err := os.Symlink("missing/../../../outside.txt", filepath.Join(workDir, "docs", "dangling-escape.md")); err != nil {
		t.Fatalf("symlink dangling-escape: %v", err)
	}
	if err := os.Symlink("loop-b.md", filepath.Join(workDir, "docs", "loop-a.md")); err != nil {
		t.Fatalf("symlink loop-a: %v", err)
	}
	if err := os.Symlink("loop-a.md", filepath.Join(workDir, "docs", "loop-b.md")); err != nil {
		t.Fatalf("symlink loop-b: %v", err)
	}

	docsManifest := contract.Manifest{
		SchemaVersion: "1",
		Name:          "fs-sec",
		Version:       1,
		Description:   "Filesystem escape security test",
		ReadOnly:      true,
		Capabilities: contract.Capabilities{
			FS: &contract.FSCapability{Read: []string{"docs/**"}},
		},
	}
	hDocs, err := host.New(docsManifest, workDir, nil, nil)
	if err != nil {
		t.Fatalf("host.New(docs): %v", err)
	}
	defer func() { _ = hDocs.Close() }()

	deniedPaths := []string{
		"../secret.txt",
		"docs/../../etc/passwd",
		"/etc/passwd",
		`docs\..\secret.txt`,
		"docs/allowed.md\x00.txt",
		"docs/ext-file.md",
		"docs/rel-ext-file.md",
		"docs/ext-dir/secret.txt",
		"docs/priv-dir/secret.txt",
		"docs/dangling-escape.md",
		"docs/loop-a.md",
		"private/secret.txt",
	}
	for _, p := range deniedPaths {
		_, readErr := hDocs.FSRead(p, host.FSReadOptions{})
		assertErrCode(t, readErr, contract.ErrCapabilityDenied)

		_, statErr := hDocs.FSStat(p)
		assertErrCode(t, statErr, contract.ErrCapabilityDenied)
	}

	// Undeclared subpath globs and symlinked dirs fail closed.
	for _, badGlob := range []string{
		"private/**",
		"secret.txt",
		"*.txt",
		"docs/ext-dir/**",
		"docs/priv-dir/**",
		"../**",
		"/etc/*",
		`docs\**`,
	} {
		_, globErr := hDocs.FSGlob(badGlob, host.FSGlobOptions{})
		assertErrCode(t, globErr, contract.ErrCapabilityDenied)
	}

	// Broad glob "**" must only return docs/allowed.md, skipping all escaping/policy-violating symlinks.
	got, globErr := hDocs.FSGlob("**", host.FSGlobOptions{})
	if globErr != nil {
		t.Fatalf("FSGlob(**): %v", globErr)
	}
	if !slices.Equal(got, []string{"docs/allowed.md"}) {
		t.Fatalf("FSGlob(**) = %v, want [docs/allowed.md]", got)
	}

	// Even with "**" FS capability, globbing through an external symlinked directory fails closed.
	allManifest := docsManifest
	allManifest.Capabilities.FS = &contract.FSCapability{Read: []string{"**"}}
	hAll, allErr := host.New(allManifest, workDir, nil, nil)
	if allErr != nil {
		t.Fatalf("host.New(all): %v", allErr)
	}
	defer func() { _ = hAll.Close() }()
	_, extDirGlobErr := hAll.FSGlob("docs/ext-dir/**", host.FSGlobOptions{})
	assertErrCode(t, extDirGlobErr, contract.ErrCapabilityDenied)
}

func TestSecurityExecCommandAndFlagInjection(t *testing.T) {
	workDir := t.TempDir()
	helperDir := t.TempDir()
	helperBin := filepath.Join(helperDir, "knurl-sec-helper")
	helperSrc := filepath.Join(helperDir, "main.go")
	helperCode := `package main
import (
	"fmt"
	"os"
	"strings"
)
func main() {
	fmt.Print(strings.Join(os.Args[1:], "|"))
}
`
	if err := os.WriteFile(helperSrc, []byte(helperCode), 0o600); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	if out, err := exec.Command("go", "build", "-o", helperBin, helperSrc).CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}
	t.Setenv("PATH", helperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := contract.Manifest{
		SchemaVersion: "1",
		Name:          "exec-sec",
		Version:       1,
		Description:   "Exec security test manifest",
		ReadOnly:      true,
		Capabilities: contract.Capabilities{
			Exec: &contract.ExecCapability{
				Templates: []contract.ExecTemplate{
					{
						ID:          "forbidden-bash",
						Program:     "bash",
						Args:        []string{"-c", "echo pwned"},
						ReadOnly:    true,
						Description: "Forbidden bash check",
					},
					{
						ID:          "forbidden-eval",
						Program:     "eval",
						Args:        []string{"echo"},
						ReadOnly:    true,
						Description: "Forbidden eval check",
					},
					{
						ID:          "dyn-args0",
						Program:     "knurl-sec-helper",
						Args:        []string{"${arg:subcmd}"},
						ArgsSchema:  map[string]contract.ExecArgSchema{"subcmd": {Pattern: "^ok$"}},
						ReadOnly:    true,
						Description: "Dynamic args[0] check",
					},
					{
						ID:          "ld-preload",
						Program:     "knurl-sec-helper",
						Args:        []string{"ok"},
						Env:         map[string]string{"LD_PRELOAD": "/tmp/evil.so"},
						ReadOnly:    true,
						Description: "Forbidden dynamic linker env",
					},
					{
						ID:      "strict-arg",
						Program: "knurl-sec-helper",
						Args:    []string{"status", "--", "${arg:ref}"},
						ArgsSchema: map[string]contract.ExecArgSchema{
							"ref": {Pattern: `^[a-zA-Z0-9._/-]+$`, MaxLength: 64},
						},
						ReadOnly:    true,
						Description: "Strict RE2 validated ref arg",
					},
					{
						ID:      "multiline-bypass-check",
						Program: "knurl-sec-helper",
						Args:    []string{"check", "${arg:val}"},
						ArgsSchema: map[string]contract.ExecArgSchema{
							// Even with inline (?m), \A...\z anchoring must reject trailing newlines.
							"val": {Pattern: `(?m)^[a-z]+$`, MaxLength: 64},
						},
						ReadOnly:    true,
						Description: "Verify full-string RE2 anchoring",
					},
					{
						ID:      "literal-argv",
						Program: "knurl-sec-helper",
						Args:    []string{"echo", "${arg:raw}"},
						ArgsSchema: map[string]contract.ExecArgSchema{
							"raw": {Pattern: `^.+$`, MaxLength: 128},
						},
						ReadOnly:    true,
						Description: "Verify shell metacharacters are passed as literal argv",
					},
				},
			},
		},
	}

	h, err := host.New(m, workDir, nil, nil)
	if err != nil {
		t.Fatalf("host.New: %v", err)
	}
	defer func() { _ = h.Close() }()

	ctx := context.Background()
	for _, id := range []string{"undeclared", "forbidden-bash", "forbidden-eval", "dyn-args0", "ld-preload"} {
		_, execErr := h.Exec(ctx, id, nil, host.ExecOptions{})
		assertErrCode(t, execErr, contract.ErrCapabilityDenied)
	}

	// Extra arg rejected.
	_, extraErr := h.Exec(ctx, "strict-arg", map[string]string{"ref": "main", "extra": "1"}, host.ExecOptions{})
	assertErrCode(t, extraErr, contract.ErrInvalidArgument)

	// Flag and shell metacharacter injections rejected by args_schema.
	for _, badRef := range []string{
		"-c core.sshCommand=id",
		"-o ProxyCommand=id",
		"; id",
		"main | cat /etc/passwd",
		"$(whoami)",
		"`id`",
		"main\n-oProxyCommand=id",
	} {
		_, badErr := h.Exec(ctx, "strict-arg", map[string]string{"ref": badRef}, host.ExecOptions{})
		assertErrCode(t, badErr, contract.ErrInvalidArgument)
	}

	// (?m) inline flag cannot bypass \A(?:...)\z full-string anchoring.
	_, nlErr := h.Exec(ctx, "multiline-bypass-check", map[string]string{"val": "ok\n;id"}, host.ExecOptions{})
	assertErrCode(t, nlErr, contract.ErrInvalidArgument)

	// Even when a template allows arbitrary non-newline text, metacharacters are passed as literal argv, never via a shell.
	metacharPayload := "hello; touch pwned | $(id) `uname`"
	litRes, litErr := h.Exec(ctx, "literal-argv", map[string]string{"raw": metacharPayload}, host.ExecOptions{})
	if litErr != nil {
		t.Fatalf("Exec(literal-argv): %v", litErr)
	}
	if litRes.Stdout != "echo|"+metacharPayload {
		t.Fatalf("expected literal argv without shell expansion, got %q", litRes.Stdout)
	}
}

func TestSecurityGitOptionInjection(t *testing.T) {
	repoDir := t.TempDir()
	runGitTest(t, repoDir, "init", "-b", "main")
	runGitTest(t, repoDir, "config", "user.name", "Tester")
	runGitTest(t, repoDir, "config", "user.email", "tester@example.com")
	runGitTest(t, repoDir, "config", "commit.gpgsign", "false")
	writeTestFile(t, repoDir, "file.txt", "v1\n")
	runGitTest(t, repoDir, "add", "file.txt")
	runGitTest(t, repoDir, "commit", "-m", "initial")

	// Inject malicious diff.external into repo .git/config; hardened --no-ext-diff must neutralize it.
	pwnSentinel := filepath.Join(repoDir, "EXT_DIFF_EXECUTED")
	runGitTest(t, repoDir, "config", "diff.external", "sh -c 'touch "+pwnSentinel+"'")
	writeTestFile(t, repoDir, "file.txt", "v2\n")

	m := contract.Manifest{
		SchemaVersion: "1",
		Name:          "git-sec",
		Version:       1,
		Description:   "Git injection security test",
		ReadOnly:      true,
		Capabilities:  contract.Capabilities{Git: &contract.GitCapability{Read: true}},
	}
	h, err := host.New(m, repoDir, nil, nil)
	if err != nil {
		t.Fatalf("host.New: %v", err)
	}
	defer func() { _ = h.Close() }()

	ctx := context.Background()
	diffRes, diffErr := h.GitDiff(ctx, host.GitDiffOptions{})
	if diffErr != nil {
		t.Fatalf("GitDiff with diff.external config failed: %v", diffErr)
	}
	if !strings.Contains(diffRes.Patch, "+v2") {
		t.Fatalf("expected normal unified patch output, got %q", diffRes.Patch)
	}
	if _, statErr := os.Stat(pwnSentinel); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("diff.external executed unexpectedly despite --no-ext-diff")
	}

	for _, badRef := range []string{"--upload-pack=id", "--exec=id", "-p", ":HEAD", "main\n--exec=id", `main\foo`} {
		_, logErr := h.GitLog(ctx, host.GitLogOptions{Ref: badRef})
		assertErrCode(t, logErr, contract.ErrInvalidArgument)

		_, dErr := h.GitDiff(ctx, host.GitDiffOptions{Base: badRef})
		assertErrCode(t, dErr, contract.ErrInvalidArgument)
	}

	for _, badPath := range []string{"--output=/tmp/pwn", "-p", ":(glob)**"} {
		_, logErr := h.GitLog(ctx, host.GitLogOptions{Path: badPath})
		assertErrCode(t, logErr, contract.ErrInvalidArgument)

		_, dErr := h.GitDiff(ctx, host.GitDiffOptions{Path: badPath})
		assertErrCode(t, dErr, contract.ErrInvalidArgument)
	}

	for _, travPath := range []string{"../outside", "/etc/passwd"} {
		_, logErr := h.GitLog(ctx, host.GitLogOptions{Path: travPath})
		assertErrCode(t, logErr, contract.ErrCapabilityDenied)

		_, dErr := h.GitDiff(ctx, host.GitDiffOptions{Path: travPath})
		assertErrCode(t, dErr, contract.ErrCapabilityDenied)
	}
}

func TestSecurityHTTPEgressSSRFAndRedirects(t *testing.T) {
	const apiSecret = "secret-bearer-token-9999"

	// 1. Policy denials and SSRF IP literals / localhost without network access.
	policyManifest := contract.Manifest{
		SchemaVersion: "1",
		Name:          "http-sec",
		Version:       1,
		Description:   "HTTP security and SSRF test",
		ReadOnly:      false,
		Secrets: []contract.SecretRef{
			{Name: "API_TOKEN", Required: true, Description: "Allowed secret"},
			{Name: "UNBOUND_TOKEN", Required: true, Description: "Declared but not bound to rule"},
		},
		Capabilities: contract.Capabilities{
			HTTP: &contract.HTTPCapability{
				Allow: []contract.HTTPRule{
					{
						Domain:       "allowed.example.test",
						Methods:      []string{"GET", "POST"},
						PathPrefixes: []string{"/healthz", "/api/"},
						Secrets:      []string{"API_TOKEN"},
					},
					{
						Domain:  "*.sub.example.test",
						Methods: []string{"GET"},
					},
				},
			},
		},
	}

	secrets := map[string]string{
		"API_TOKEN":     apiSecret,
		"UNBOUND_TOKEN": "unbound-secret-1234",
	}

	// Wire SSRF DNS mock for dialer CIDR testing.
	var nextLookupIPs []net.IPAddr
	hDial, err := host.NewWithOptions(
		policyManifest,
		t.TempDir(),
		secrets,
		nil,
		host.WithHTTPLookupIP(func(context.Context, string) ([]net.IPAddr, error) {
			return nextLookupIPs, nil
		}),
	)
	if err != nil {
		t.Fatalf("host.NewWithOptions: %v", err)
	}
	defer func() { _ = hDial.Close() }()

	ctx := context.Background()

	// Capability denials: undeclared host, http:// when allow_http=false, wrong path boundary, wildcard depth, unbound secret.
	for _, deniedURL := range []string{
		"https://evil.example.test/healthz",
		"http://allowed.example.test/healthz",
		"file:///etc/passwd",
		"ftp://allowed.example.test/healthz",
		"https://allowed.example.test/admin",
		"https://allowed.example.test/healthz-admin",
		"https://allowed.example.test/healthz/../admin",
		"https://allowed.example.test/healthz/%2e%2e/admin",
		"https://sub.example.test/ok",
		"https://a.b.sub.example.test/ok",
	} {
		_, getErr := hDial.HTTPGet(ctx, deniedURL, host.HTTPOptions{})
		assertErrCode(t, getErr, contract.ErrCapabilityDenied)
	}

	// Disallowed method on GET-only wildcard rule.
	_, postErr := hDial.HTTPPost(ctx, "https://v1.sub.example.test/ok", []byte("{}"), true, host.HTTPOptions{})
	assertErrCode(t, postErr, contract.ErrCapabilityDenied)

	// Host header override rejected.
	_, hostHdrErr := hDial.HTTPGet(ctx, "https://allowed.example.test/healthz", host.HTTPOptions{
		Headers: map[string]string{"Host": "evil.example.test"},
	})
	assertErrCode(t, hostHdrErr, contract.ErrInvalidArgument)

	// Undeclared secret or secret missing from rule.secrets rejected.
	_, undSecErr := hDial.HTTPGet(ctx, "https://allowed.example.test/healthz", host.HTTPOptions{
		Headers: map[string]string{"Authorization": "Bearer ${secret:UNKNOWN_SECRET}"},
	})
	assertErrCode(t, undSecErr, contract.ErrCapabilityDenied)

	_, unboundSecErr := hDial.HTTPGet(ctx, "https://allowed.example.test/healthz", host.HTTPOptions{
		Headers: map[string]string{"Authorization": "Bearer ${secret:UNBOUND_TOKEN}"},
	})
	assertErrCode(t, unboundSecErr, contract.ErrCapabilityDenied)

	// SSRF literals and localhost rejected with HTTP_ERROR.
	for _, ssrfURL := range []string{
		"https://127.0.0.1/healthz",
		"https://169.254.169.254/latest/meta-data/",
		"https://[::1]/healthz",
		"https://localhost/healthz",
		"https://api.localhost/healthz",
	} {
		_, ssrfErr := hDial.HTTPGet(ctx, ssrfURL, host.HTTPOptions{})
		assertErrCode(t, ssrfErr, contract.ErrHTTPError)
	}

	// SSRF DNS resolution CIDR blocklist through real ssrfSafeDialContext.
	blockedCIDRs := [][]string{
		{"127.0.0.1"},
		{"10.0.0.1"},
		{"172.16.0.1"},
		{"192.168.1.1"},
		{"169.254.169.254"},
		{"100.64.0.1"},
		{"0.0.0.0"},
		{"::1"},
		{"fc00::1"},
		{"fe80::1"},
		{"::ffff:127.0.0.1"},
		{"93.184.216.34", "127.0.0.1"}, // mixed public + private must fail closed!
	}
	for _, ipList := range blockedCIDRs {
		nextLookupIPs = make([]net.IPAddr, 0, len(ipList))
		for _, rawIP := range ipList {
			nextLookupIPs = append(nextLookupIPs, net.IPAddr{IP: net.ParseIP(rawIP)})
		}
		_, dialErr := hDial.HTTPGet(ctx, "https://allowed.example.test/healthz", host.HTTPOptions{})
		assertErrCode(t, dialErr, contract.ErrHTTPError)
	}

	// 2. Real httptest.Server redirect, POST JSON, timeout, body cap, and secret redaction tests.
	var evilHits atomic.Int32
	evilSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		evilHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("evil-reached"))
	}))
	defer evilSrv.Close()

	var adminHits atomic.Int32
	var hop2AuthHeader atomic.Value
	allowedSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz/redirect-cross":
			http.Redirect(w, r, "https://evil.example.test/steal", http.StatusFound)
		case "/healthz/redirect-admin":
			http.Redirect(w, r, "https://allowed.example.test/admin", http.StatusFound)
		case "/healthz/redirect-downgrade":
			http.Redirect(w, r, "http://allowed.example.test/healthz/ok", http.StatusFound)
		case "/healthz/redirect-same":
			http.Redirect(w, r, "https://allowed.example.test/healthz/ok", http.StatusFound)
		case "/healthz/ok":
			hop2AuthHeader.Store(r.Header.Get("Authorization"))
			w.Header().Set("X-Echo-Secret", apiSecret)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok echo=" + apiSecret))
		case "/admin":
			adminHits.Add(1)
			w.WriteHeader(http.StatusOK)
		case "/api/post":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("ct=" + r.Header.Get("Content-Type")))
		case "/healthz/slow":
			time.Sleep(250 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		case "/healthz/huge":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.Repeat("Z", host.MaxHTTPBodyBytes+4096)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer allowedSrv.Close()

	rt := &hostRoutingTransport{
		routes: map[string]*httptest.Server{
			"allowed.example.test": allowedSrv,
			"evil.example.test":    evilSrv,
		},
	}

	httpManifest := policyManifest
	httpManifest.Capabilities.HTTP.Allow[0].AllowHTTP = true // enables testing HTTPS -> HTTP downgrade block explicitly

	hHTTP, err := host.NewWithOptions(httpManifest, t.TempDir(), secrets, nil, host.WithHTTPTransport(rt))
	if err != nil {
		t.Fatalf("host.NewWithOptions(http): %v", err)
	}
	defer func() { _ = hHTTP.Close() }()

	// Cross-domain 302 redirect fails closed with zero hits on evilSrv.
	_, crossErr := hHTTP.HTTPGet(ctx, "https://allowed.example.test/healthz/redirect-cross", host.HTTPOptions{})
	assertErrCode(t, crossErr, contract.ErrHTTPError)
	if evilHits.Load() != 0 {
		t.Fatalf("evilSrv was contacted (%d hits) on cross-domain redirect", evilHits.Load())
	}

	// Path-prefix escape 302 redirect fails closed with zero hits on /admin.
	_, adminErr := hHTTP.HTTPGet(ctx, "https://allowed.example.test/healthz/redirect-admin", host.HTTPOptions{})
	assertErrCode(t, adminErr, contract.ErrHTTPError)
	if adminHits.Load() != 0 {
		t.Fatalf("/admin handler was reached (%d hits) via redirect", adminHits.Load())
	}

	// HTTPS -> HTTP downgrade redirect fails closed.
	_, downErr := hHTTP.HTTPGet(ctx, "https://allowed.example.test/healthz/redirect-downgrade", host.HTTPOptions{})
	assertErrCode(t, downErr, contract.ErrHTTPError)

	// Same-domain allowed redirect succeeds, strips secret Authorization header on hop 2, and redacts response body + headers.
	redirRes, redirErr := hHTTP.HTTPGet(ctx, "https://allowed.example.test/healthz/redirect-same", host.HTTPOptions{
		Headers: map[string]string{"Authorization": "Bearer ${secret:API_TOKEN}"},
	})
	if redirErr != nil {
		t.Fatalf("same-domain redirect failed: %v", redirErr)
	}
	if redirRes.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200", redirRes.Status)
	}
	if gotAuth, _ := hop2AuthHeader.Load().(string); gotAuth != "" {
		t.Fatalf("expected Authorization header stripped on redirect, got %q", gotAuth)
	}
	if strings.Contains(redirRes.Body, apiSecret) || !strings.Contains(redirRes.Body, "[REDACTED:API_TOKEN]") {
		t.Fatalf("expected redacted response body, got %q", redirRes.Body)
	}
	if hdr := redirRes.Headers["x-echo-secret"]; strings.Contains(hdr, apiSecret) || hdr != "[REDACTED:API_TOKEN]" {
		t.Fatalf("expected redacted lowercased response header, got %q", hdr)
	}

	// POST auto-sets Content-Type: application/json.
	postRes, postOK := hHTTP.HTTPPost(ctx, "https://allowed.example.test/api/post", []byte(`{"ping":true}`), true, host.HTTPOptions{})
	if postOK != nil {
		t.Fatalf("HTTPPost failed: %v", postOK)
	}
	if postRes.Status != http.StatusCreated || postRes.Body != "ct=application/json" {
		t.Fatalf("unexpected POST response: %+v", postRes)
	}

	// Per-request timeout_ms triggers TIMEOUT.
	_, timeoutErr := hHTTP.HTTPGet(ctx, "https://allowed.example.test/healthz/slow", host.HTTPOptions{TimeoutMs: 40, HasTimeout: true})
	assertErrCode(t, timeoutErr, contract.ErrTimeout)

	// 1 MiB response body cap sets Truncated=true.
	hugeRes, hugeErr := hHTTP.HTTPGet(ctx, "https://allowed.example.test/healthz/huge", host.HTTPOptions{})
	if hugeErr != nil {
		t.Fatalf("HTTPGet huge failed: %v", hugeErr)
	}
	if !hugeRes.Truncated || len(hugeRes.Body) != host.MaxHTTPBodyBytes {
		t.Fatalf("expected truncated 1 MiB body, got len=%d truncated=%v", len(hugeRes.Body), hugeRes.Truncated)
	}
}

func TestSecuritySecretRedactionEdgeCases(t *testing.T) {
	multiLineSecret := "-----BEGIN PRIVATE KEY-----\r\nline-two-secret-material\r\n-----END PRIVATE KEY-----"
	jsonSpecialSecret := `sec"ret\with` + "\n" + `quotes`
	redactor := host.NewRedactor(map[string]string{
		"PEM_KEY":    multiLineSecret,
		"JSON_SEC":   jsonSpecialSecret,
		"EMPTY_SEC":  "",
		"SPACE_SEC":  "     ",
		"SHORT_SEC":  "abc",
		"NORMAL_SEC": "alpha-bravo-charlie",
	})

	// Empty, whitespace-only, and <4-char secrets must never corrupt benign strings.
	benign := "hello abc   world"
	if got := redactor.String(benign); got != benign {
		t.Fatalf("benign string mutated by empty/short secrets: %q -> %q", benign, got)
	}

	// Multi-line full block and individual lines redacted cleanly.
	leakedFull := "full=" + multiLineSecret + " norm=" + strings.ReplaceAll(multiLineSecret, "\r\n", "\n") + " line=line-two-secret-material"
	scrubbedMulti := redactor.String(leakedFull)
	if strings.Contains(scrubbedMulti, "BEGIN PRIVATE KEY") || strings.Contains(scrubbedMulti, "line-two-secret-material") {
		t.Fatalf("multi-line secret leaked: %q", scrubbedMulti)
	}
	if strings.Count(scrubbedMulti, "[REDACTED:PEM_KEY]") != 3 {
		t.Fatalf("expected 3 PEM_KEY redactions, got %q", scrubbedMulti)
	}

	// JSON-escaped form redacted and WorkerResponse JSON structure stays valid.
	jsonPayload, err := json.Marshal(map[string]any{"leaked": jsonSpecialSecret})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	scrubbedJSONText := redactor.String(string(jsonPayload))
	if strings.Contains(scrubbedJSONText, "quotes") || !json.Valid([]byte(scrubbedJSONText)) {
		t.Fatalf("JSON-escaped secret leaked or corrupted JSON: %q", scrubbedJSONText)
	}

	resp := &contract.WorkerResponse{
		OK: true,
		Output: map[string]any{
			"nested": map[string]any{
				"key": jsonSpecialSecret,
				"arr": []any{"prefix-" + multiLineSecret, "alpha-bravo-charlie"},
			},
		},
		Error: &contract.ToolError{
			Code:    contract.ErrToolError,
			Message: "failed with " + jsonSpecialSecret,
			Details: map[string]any{"pem": multiLineSecret},
		},
		Diagnostics: []string{"diag line-two-secret-material"},
	}
	redactor.WorkerResponse(resp)

	encoded, encErr := json.Marshal(resp)
	if encErr != nil || !json.Valid(encoded) {
		t.Fatalf("redacted WorkerResponse invalid JSON: %v", encErr)
	}
	encodedStr := string(encoded)
	for _, secretFrag := range []string{"line-two-secret-material", "BEGIN PRIVATE KEY", "alpha-bravo-charlie", `sec"ret`} {
		if strings.Contains(encodedStr, secretFrag) {
			t.Fatalf("secret fragment %q leaked in WorkerResponse JSON: %s", secretFrag, encodedStr)
		}
	}
}
