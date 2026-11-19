# Security Model

`knurl` assumes that **AI agents can be prompt-injected, confused by untrusted repo files, or mistaken**, and that **tool source (`tool.js`) may be authored by an untrusted agent before human review**. All security boundaries are enforced in Go outside the JavaScript VM.

## 1. Threat Model & Trust Boundaries

| Threat | Mitigation |
| :--- | :--- |
| **Silent tool installation or tampering** | Agents can only write to staging (`knurl_propose`). Only human CLI commands (`knurl approve`) write to active `tools/` directories. |
| **Sneaky privilege escalation on tool repair (`v2`)** | `knurl approve` computes and highlights every added/widened capability (`capability_diff`) and whether the tool became `elevated`. |
| **Shell / argument / flag injection** | No shell invocation (`sh -c` forbidden). `knurl.exec` binds literal `args[0]` and RE2-validates every `${arg:...}` placeholder before `exec.CommandContext`. |
| **Symlink escape / arbitrary file read** | `knurl.fs` confines every path operation through Go `os.Root` anchored to `cwd` and checks relative glob patterns. |
| **Secret theft by LLM or malicious JS** | Secrets never enter `tool.js` or MCP responses; resolved by Go host only inside `exec` args/env and allowlisted HTTPS headers; scrubbed from all outputs. |
| **SSRF & cloud metadata access** | `knurl.http` rejects IP literals, `localhost`, private/link-local/metadata IPs after DNS resolution, HTTP (by default), and cross-domain redirects. |
| **Runaway JS loops / orphan processes** | `_worker` subprocess runs with `context.WithTimeout`, `goja` interrupt, Unix process-group (`-pgid`) kill, and bounded output/IPC byte limits. |

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

Narrowing or unchanged capabilities never set `has_escalation: true`.

## 4. Filesystem Confinement (`os.Root`)

- Every `knurl.fs` operation opens the working directory via Go `os.OpenRoot(cwd)`.
- Paths starting with `/` or containing `..` segments are rejected immediately with `CAPABILITY_DENIED`.
- `os.Root` prevents symlinks inside the workspace from escaping outside `cwd`.
- Every resolved relative path must match at least one glob pattern in `manifest.capabilities.fs.read`.

## 5. Constrained `knurl.exec` Templates

Bare binary allowlists (such as allowing `"git"` or `"ssh"` with arbitrary args) are **forbidden** because flags like `git -c core.sshCommand=...` or `ssh -o ProxyCommand=...` bypass read-only intent.

Every `ExecTemplate` enforces:
1. **Forbidden shell binaries:** `program` cannot be `sh`, `bash`, `zsh`, `dash`, `fish`, `ksh`, `csh`, `tcsh`, `cmd`, `cmd.exe`, `powershell`, `powershell.exe`, `pwsh`, or `eval`, and cannot contain `/` or `\`.
2. **Literal subcommand / first argument:** `args[0]` must be non-empty and must contain **zero** `${...}` placeholders, locking the subcommand or initial flag.
3. **Mandatory RE2 argument validation:** Every `${arg:<key>}` placeholder in `args` must have an entry in `args_schema` with a valid Go RE2 `pattern` (checked anchored `^(?:pattern)$`), optional `max_length`, and optional `enum`.
4. **Single-pass expansion:** Placeholders `${arg:<key>}` and `${secret:<NAME>}` are expanded once directly into the final `argv` slice passed to `os/exec`—substituted values are never re-scanned for placeholders or parsed by a shell.
5. **Minimal sanitized environment:** Child processes receive only a minimal deterministic environment (`PATH`, `HOME`, `LANG`, `LC_ALL`, `TERM`, `TZ`), plus explicit template `env` bindings—never the parent's full `os.Environ()`.

## 6. HTTP Egress & Secret Protection

- **Domain allowlist & HTTPS default:** Every request URL must match a declared `HTTPRule.domain` (exact match or single-label `*.example.com`), `methods`, and `path_prefixes` (if specified). Plaintext `http://` is denied unless `allow_http: true`.
- **SSRF defense:** Wildcard `*`, `localhost`, and IP literals in `domain` are rejected by `manifest.schema.json`. At request time, the Go `net.Dialer` checks resolved IPs and blocks loopback (`127.0.0.0/8`, `::1`), private (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`), and link-local / cloud metadata (`169.254.0.0/16`, `fe80::/10`) ranges.
- **Host-only secret binding:** HTTP rules only resolve `${secret:NAME}` in request headers when `NAME` is explicitly listed in that rule's `secrets` array. Cross-domain redirects are rejected and secret headers are stripped on any redirect.
- **Secret redaction:** Resolved secret strings (min length 4, plus base64 and URL-encoded forms) are replaced with `[REDACTED:<NAME>]` across `WorkerResponse.Output`, `WorkerResponse.Error`, `WorkerResponse.Diagnostics`, and `knurl.exec` `stdout`/`stderr` before leaving the Go boundary.

## 7. Automated Dry-Run Safety Gate

During `knurl_propose`, `knurl` automatically performs a working-directory dry-run preview **only when all of the following hold**:
1. `manifest.read_only == true`
2. Every `exec.templates[*].read_only == true`
3. Every `http.allow[*].methods` contains only `GET`

Otherwise, `dry_run.executed` is `false` with an explanatory `skipped_reason` (`"non-read-only tool or capability"`).
