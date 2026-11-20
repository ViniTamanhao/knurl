# Security Model

`knurl` assumes that **AI agents can be prompt-injected, confused by untrusted repo files, or mistaken**, and that **tool source (`tool.js`) may be authored by an untrusted agent before human review**. All security boundaries are enforced in Go outside the JavaScript VM.

## 1. Threat Model & Trust Boundaries

| Threat | Mitigation |
| :--- | :--- |
| **Silent tool installation or tampering** | Agents can only write to staging (`knurl_propose`). Only human CLI commands (`knurl approve`) write to active `tools/` directories. |
| **Sneaky privilege escalation on tool repair (`v2`)** | `knurl approve` computes and highlights every added/widened capability (`capability_diff`) and whether the tool became `elevated`. |
| **Shell / argument / flag injection** | No shell invocation (`sh -c` forbidden). `knurl.exec` binds literal `args[0]` and `\A(?:...)\z`-anchored RE2 validates every `${arg:...}` placeholder before `exec.CommandContext`. |
| **Symlink escape / arbitrary file read** | `knurl.fs` confines every path operation through Go `os.Root` anchored to `cwd`, checks symlink targets and literal glob prefixes, and enforces `capabilities.fs.read` pattern overlap. |
| **Secret theft by LLM or malicious JS** | Secrets never enter `tool.js` or MCP responses; resolved by Go host only inside `exec` args/env and allowlisted HTTPS headers; scrubbed (raw, multi-line, JSON-escaped, base64, URL-encoded) from all outputs. |
| **SSRF & cloud metadata access** | `knurl.http` rejects IP literals, `localhost`, private/link-local/CGNAT/metadata IPs after DNS resolution, HTTP (by default), cross-domain redirects, and HTTPS->HTTP downgrades. |
| **Runaway JS loops / memory bombs / orphan processes** | `_worker` subprocess runs with `context.WithTimeout`, `goja` interrupt + call-stack cap, `debug.SetMemoryLimit`, active `HeapAlloc` delta watchdog, Unix `RLIMIT_CORE=0`/`RLIMIT_NOFILE<=256`, `-pgid` process-group cleanup, and bounded output/IPC byte limits. |

## 2. Approve-Once Trust Boundary & Derived `elevated` Flag

An MCP client needs zero Bash/terminal permissions to invoke `knurl_run`. Human trust is established **once per version** during `knurl approve`:

1. `knurl` derives **`elevated`** strictly from `manifest.json` on the host—tools cannot opt out or self-declare `elevated: false`:
   ```text
   elevated = !read_only
           || len(capabilities.exec.templates) > 0
           || len(capabilities.http.allow) > 0
           || len(secrets) > 0
   ```
   Pure local read-only inspection (`fs.read` + `git.read` with `read_only: true`) is non-elevated; any external command, network egress, secret binding, or write capability is prominently marked `ELEVATED` during approval.
2. Before promotion, `knurl approve` verifies the proposal's `status == "pending"` and recomputes the SHA-256 digest of staged `manifest.json`, `tool.js`, and `tests.json` against `proposal.json` to detect tampering between `propose` and `approve`.

## 3. Capability-Escalation Diffing

When proposing `v<N+1>` over an existing active `v<N>`, `knurl` diffs the normalized capability sets:
- `read_only`: `true -> false` (`has_escalation: true`)
- `fs.read`: newly added path patterns
- `git.read`: `false -> true`
- `http.allow`: newly added domains, methods (`GET -> POST`), `allow_http: false -> true`, widened `path_prefixes`, or added bound secrets
- `exec.templates`: newly added templates, changed `program`, changed `args`, widened `args_schema` regex/max_length/enum, added `env`, or `read_only: true -> false`
- `secrets`: newly added secrets or `required: false -> true`

Proven narrowing and unchanged capabilities never set `has_escalation: true`.
Descriptions and enum ordering are metadata, not permissions. Containment checks
recognize HTTP method/path/domain/secret subsets, stricter argument lengths and
enum subsets, and optional secret transitions. Arbitrary regex/glob inclusion is
not inferred: changes whose containment cannot be proved require escalation review.

## 4. Filesystem Confinement (`os.Root`)

- Every `knurl.fs` operation opens the working directory via Go `os.OpenRoot(cwd)`.
- Paths starting with `/`, containing `\`, containing NUL bytes, or containing `..` segments are rejected immediately with `CAPABILITY_DENIED`.
- `resolveWithinRoot` walks components via `root.Lstat` and `root.Readlink` (normalizing `..` segments even through dangling paths), rejecting any symlink escaping outside `cwd`, symlink loops (`> 40` hops), or symlinks resolving outside `manifest.capabilities.fs.read`. `FSRead` and `FSStat` open/stat the resolved canonical path inside `h.root`.
- `FSGlob` verifies `GlobIntersectsAny(patterns, requestedPattern)` and resolves any leading literal directory prefix through `resolveWithinRoot` before walking, immediately rejecting undeclared subpath globs and external symlinked directories with `CAPABILITY_DENIED`.

## 5. Constrained `knurl.exec` Templates & Read-Only `knurl.git`

Bare binary allowlists (such as allowing `"git"` or `"ssh"` with arbitrary args) are **forbidden** because flags like `git -c core.sshCommand=...` or `ssh -o ProxyCommand=...` bypass read-only intent.

Every `ExecTemplate` enforces:
1. **Strict program names & forbidden shell binaries:** `program` must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`, cannot contain `/` or `\`, and cannot be `sh`, `bash`, `zsh`, `dash`, `fish`, `ksh`, `csh`, `tcsh`, `cmd`, `cmd.exe`, `powershell`, `powershell.exe`, `pwsh`, or `eval`.
2. **Literal subcommand / first argument:** `args[0]` must be non-empty and must contain **zero** `${...}` placeholders, locking the subcommand or initial flag.
3. **Full-string RE2 argument validation:** Every `${arg:<key>}` placeholder in `args` must exist in `args_schema` with a valid Go RE2 `pattern` anchored as `\A(?:pattern)\z` (so inline `(?m)` cannot bypass anchoring via newlines), optional `max_length`, and optional `enum`.
4. **Single-pass expansion:** Placeholders `${arg:<key>}` and `${secret:<NAME>}` are expanded once directly into the final `argv` slice passed to `os/exec`—substituted values are never re-scanned for placeholders or parsed by a shell.
5. **Minimal sanitized environment & linker guard:** Child processes receive only a minimal deterministic environment (`PATH`, `HOME`, `LANG`, `LC_ALL`, `TERM`, `TZ`), plus explicit template `env` bindings—never the parent's full `os.Environ()`. Dynamic-linker env overrides (`LD_PRELOAD`, `LD_LIBRARY_PATH`, `DYLD_INSERT_LIBRARIES`, `DYLD_LIBRARY_PATH`, `DYLD_FRAMEWORK_PATH`) are rejected with `CAPABILITY_DENIED`.

Read-only `knurl.git` invocations additionally enforce:
- Global hardening flags (`--no-pager`, `-c core.fsmonitor=false`, `-c core.quotepath=false`, `-c protocol.ext.allow=never`), `GIT_CONFIG_NOSYSTEM=1`, and `--no-ext-diff --no-textconv` on `diff` (neutralizing malicious `.git/config` external diff drivers).
- Mandatory `--` option/pathspec separators on `git log` and `git diff`, plus `validateGitRef` / `validateGitPath` rejecting leading `-` or `:`, backslashes, whitespace, and control characters.

## 6. HTTP Egress & Secret Protection

- **Domain allowlist & HTTPS default:** Every request URL is validated for scheme, hostname, control chars, userinfo, and `..` / `\` path traversal, and must match a declared `HTTPRule.domain` (exact match or single-label `*.example.com`), `methods` (`GET` / `POST`), and boundary-aware `path_prefixes` (if specified). Plaintext `http://` is denied unless `allow_http: true`. Overriding `Host` or injecting CRLF in headers is rejected with `INVALID_ARGUMENT`.
- **SSRF defense:** Wildcard `*`, `localhost`, `*.localhost`, and IP literals are rejected before dialing (`HTTP_ERROR`). The custom `ssrfSafeDialContext` resolves DNS via `LookupIPAddr`, validates **every** returned address (blocking loopback `127.0.0.0/8`, `::1`; private `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`; link-local/metadata `169.254.0.0/16`, `fe80::/10`; unspecified `0.0.0.0/8`, `::`; CGNAT `100.64.0.0/10`; `198.18.0.0/15`; `240.0.0.0/4`; NAT64 `64:ff9b::/96`, `64:ff9b:1::/48`; discard `100::/64`; and multicast), and dials the validated IP directly (blocking DNS rebinding TOCTOU).
- **Redirect isolation & secret stripping:** Custom `CheckRedirect` caps hops at 10, rejects **all** cross-domain redirects (`req.URL.Hostname() != orig.URL.Hostname()`), rejects HTTPS->HTTP downgrades, re-validates every hop URL against `capabilities.http.allow`, and strips secret-bound headers plus `Authorization`, `Proxy-Authorization`, and `Cookie` on every redirect hop. Request and retained response bodies are capped at **1 MiB** (`MaxHTTPRequestBytes` / `MaxHTTPBodyBytes`).
- **Host-only secret binding:** HTTP rules only resolve `${secret:NAME}` in request headers when `NAME` is listed in both `manifest.secrets` and that matching rule's `secrets` array.
- **Defense-in-depth secret redaction:** Resolved secrets (minimum trimmed length 4; empty and whitespace-only values ignored) plus CRLF/LF normalized forms, multi-line constituent lines (`>= 4` chars), JSON-escaped string forms, base64 (`Std`, `RawStd`, `URL`, `RawURL`), and URL (`QueryEscape`, `PathEscape`) variants are replaced longest-first with `[REDACTED:<NAME>]` across `WorkerResponse.Output` (preserving valid JSON structure), `WorkerResponse.Error`, `WorkerResponse.Diagnostics`, `knurl.exec` streams, `knurl.git` patch/errors, and `knurl.http` headers/body/errors.

## 7. Automated Dry-Run Safety Gate

During `knurl_propose`, `knurl` automatically performs a working-directory dry-run preview **only when all of the following hold**:
1. `manifest.read_only == true`
2. `manifest.IsElevated() == false`: no exec templates, outbound HTTP rules,
   or declared secrets (even optional secrets make a tool elevated).
3. A fixture sample, schema-default object, or `{}` passes `input_schema`.

Otherwise, `dry_run.executed` is `false` with an explicit `skipped_reason`.
Elevated tools use `"elevated tool: exec, external HTTP, secrets, or non-read-only declaration"`;
missing safe input uses `"no schema-valid sample or default input"`. Secrets are
never resolved for elevated previews, including when a required secret is unset.

Fixture execution always uses `_worker` with an isolated temporary filesystem,
synthetic named secrets, and explicit Git/HTTP/exec mocks. Missing mocks fail
closed; they never fall through to real commands or networking. Mock responses
still pass through capability, argument, byte-cap, redaction and output-schema
checks. The temporary filesystem is removed after execution. No real secret is
used in a fixture. Source top-level code also executes only in the worker.

Capability diffs sort capability names and serialized permission details, reporting
`added`, `removed`, and `unchanged` entries. HTTP rule details include methods,
paths, plaintext permission and bound secret names; exec details include complete
argv templates, argument constraints and environment bindings. Changed rules
appear as removal plus addition. Additions not covered by previous permissions
are flagged as escalations; transition into elevated access is also flagged. Human review
includes a unified source diff. Approval rechecks the exact staged SHA-256 digest
and the active base under a registry write lock before promotion. Neither approval
nor rollback is registered as an MCP handler.

## 8. `_worker` Resource Safeguards & Adversarial Test Suite

Inside every `knurl _worker` subprocess:
- **Unix resource limits (`rlimit_unix.go`):** `RLIMIT_CORE` is set to `0` (preventing memory core dumps containing secrets) and `RLIMIT_NOFILE` soft limit is capped at `256`. `RLIMIT_AS` is deliberately left unbounded so Go runtime virtual memory reservations and `go test -race` shadow memory operate reliably.
- **Go memory & VM call-stack caps:** `debug.SetMemoryLimit(128 MiB)` bounds Go runtime GC pressure, an active `HeapAlloc` delta watchdog interrupts `goja` with `LIMIT_EXCEEDED` when VM execution grows the heap by more than `64 MiB`, and `vm.SetMaxCallStackSize(1024)` terminates unbounded recursion cleanly.
- **Graceful-then-forced `-pgid` teardown:** `Supervisor` grants `_worker` a `350 ms` grace window beyond `timeout_ms` so `_worker` catches `SIGTERM`/timeouts, terminates any nested `knurl.exec` child process groups (`SIGKILL` to `-pgid`), and emits structured `TIMEOUT` JSON before parent fallback `SIGKILL` runs.

Automated adversarial verification lives in `internal/host/security_test.go` and `internal/runtime/adversarial_test.go`, covering all 10 threat categories under `go test -race ./...`:
1. CPU infinite loops & zero orphan child process leaks (`ESRCH` PID check)
2. Exponential string bombs, 64 MiB heap array allocation bombs, and unbounded recursion
3. Oversized IPC return payloads (`> 2 MiB`), `knurl.log` floods, and `knurl.exec` stdout/stderr floods
4. Filesystem traversal (`..`, `/`, NUL), external file/directory symlinks, dangling `..` symlinks, symlink loops, and undeclared subpath globs
5. `knurl.exec` undeclared templates, shell binaries, `LD_PRELOAD` injection, `(?m)` newline regex bypasses, flag injection (`-c`, `-o`), and shell metacharacters
6. `knurl.git` option/pathspec injection and malicious `.git/config` `diff.external` neutralization
7. `knurl.http` domain/scheme/method/path escapes, `Host` header spoofing, SSRF IP/CIDR blocks, and `httptest.Server` 302 cross-domain / path-prefix / HTTPS->HTTP redirect bypasses + header stripping
8. Secret exfiltration via `exec`, HTTP response body/headers, JS exceptions, nested JSON output, `knurl.log`, and multi-line secrets
9. Pre-spawn `INVALID_INPUT` schema rejection (verified without spawning `_worker`) and post-redaction `INVALID_OUTPUT` rejection
10. Two-tier registry precedence (`repo` shadowing `global`) and path-traversal tool name rejection (`../../etc/passwd`)

Staging also compiles both embedded I/O schemas before fixture execution, rejecting
malformed schema keywords as `INVALID_MANIFEST`. The schema compiler has no external
URL loader, so schema references cannot open files or initiate network requests.
