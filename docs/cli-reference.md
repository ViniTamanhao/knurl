# CLI Reference

This document specifies the command-line interface, flags, environment variables, exit codes, and machine-readable output envelopes of the `knurl` binary.

---

## 1. Command Summary

```text
knurl <command> [flags]
```

| Command | Purpose |
| :--- | :--- |
| `knurl run <tool-name-or-path> [flags]` | Execute a tool bundle inside an isolated `_worker` subprocess |
| `knurl mcp [flags]` | Start the stdio Model Context Protocol (MCP) server |
| `knurl version` (`-v`, `--version`) | Print `knurl` version information |
| `knurl help` (`-h`, `--help`) | Show usage and command summary |
| `knurl _worker` *(internal)* | Hidden isolated worker subprocess invoked by the parent control plane |

---

## 2. `knurl run`

Resolves a tool bundle by name or direct path, validates input against `manifest.input_schema`, executes `tool.js` in an isolated `_worker` subprocess, redacts declared secrets, validates output against `manifest.output_schema`, and prints the result.

### Usage

```sh
knurl run <tool-name-or-path> [flags]
```

### Target Resolution

- **Direct path** (contains `/` or `\`, or equals `.` / `..`): Loads the bundle directory directly (`scope: "override"`).
- **Tool name** (kebab-case `^[a-z][a-z0-9]*(-[a-z0-9]+)*$`, 2–64 chars):
  - When `--tools-dir` or `$KNURL_TOOLS_DIR` is set, resolves from that directory (`scope: "override"`).
  - Otherwise resolves across the two-tier registry according to `--scope`:
    - `auto` *(default)*: walks upward from `--workdir` for `./.knurl/tools/` (stopping at `.git` or filesystem root), then falls back to the global tools directory.
    - `repo`: searches only `./.knurl/tools/`.
    - `global`: searches only the global tools directory.

### Flags

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--input <json>` | `{}` | Raw JSON object passed to `main(input)` |
| `--arg <key=value>` | — | Set a typed input field (repeatable; coerced according to `manifest.input_schema`; overrides keys in `--input`) |
| `--scope <auto\|repo\|global>` | `auto` | Registry tier to resolve the tool from |
| `--version <N\|vN>` | active / latest | Explicit immutable `v<N>` version to execute |
| `--tools-dir <dir>` | `$KNURL_TOOLS_DIR` | Override registry directory (bypasses two-tier lookup) |
| `--workdir <dir>` | current directory | Workspace directory exposed to `knurl.fs`, `knurl.git`, and `knurl.exec` |
| `--timeout-ms <ms>` | manifest default (`30000`) | Override execution timeout in milliseconds (`100`..`120000`) |
| `--json` | `false` | Emit the full `ExecutionResult` JSON envelope instead of bare output |
| `-h`, `--help` | — | Show help for `knurl run` |

### Output Streams & `--json` Envelope

- **Diagnostics (`knurl.log`):** Written to `stderr` prefixed with `[knurl] `.
- **Default mode (without `--json`):**
  - On success: writes the tool's validated `output` JSON object to `stdout`.
  - On failure: writes `knurl: <CODE>: <message>` to `stderr`.
- **Machine mode (`--json`):**
  - On success: writes the full `ExecutionResult` JSON object to `stdout`.
  - On failure: writes the full `ExecutionResult` JSON object (with `ok: false` and `error`) to `stderr`.

```json
{
  "ok": true,
  "tool": "repo-context",
  "version": 1,
  "scope": "override",
  "duration_ms": 42,
  "output": { "...": "validated against manifest.output_schema" },
  "diagnostics": ["optional redacted knurl.log lines"]
}
```

---

## 3. `knurl mcp`

Starts the stdio MCP server using `github.com/modelcontextprotocol/go-sdk/mcp`. JSON-RPC framing owns `stdout`; parent diagnostics (`slog` at `WARN` level) write exclusively to `stderr`. Terminates cleanly on `SIGINT` or `SIGTERM`.

### Usage

```sh
knurl mcp [flags]
```

### Flags

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--tools-dir <dir>` | `$KNURL_TOOLS_DIR` | Override registry directory (bypasses two-tier lookup) |
| `--workdir <dir>` | current directory | Workspace directory exposed to tool executions |
| `-h`, `--help` | — | Show help for `knurl mcp` |

See [`specs/mcp-protocol.md`](specs/mcp-protocol.md) for MCP meta-tool schemas and error codes.

---

## 4. `knurl _worker` (Internal)

Hidden subprocess entrypoint invoked by `internal/runtime.Supervisor` via `os.Executable()`. Reads a single NDJSON `WorkerRequest` frame from `stdin` (max 2 MiB), executes `tool.js` inside a synchronous `goja` VM with resource limits and host capability enforcement, and writes a single NDJSON `WorkerResponse` frame to `stdout` (max 2 MiB). Not intended for direct human invocation.

---

## 5. Environment Variables

| Variable | Precedence / Effect |
| :--- | :--- |
| `KNURL_TOOLS_DIR` | Default value for `--tools-dir` when the flag is omitted (explicit override tier) |
| `KNURL_HOME` | 1st precedence for global config root (`$KNURL_HOME/tools/`, `$KNURL_HOME/staging/`) |
| `KNURL_CONFIG_DIR` | 2nd precedence for global config root (`$KNURL_CONFIG_DIR/tools/`) |
| `XDG_CONFIG_HOME` | 3rd precedence for global config root (`$XDG_CONFIG_HOME/knurl/tools/`) |
| `HOME` | 4th precedence / fallback (`$HOME/.config/knurl/tools/`) |
| `<SECRET_NAME>` | Resolved by the parent host only when declared in `manifest.secrets`; never inherited into `_worker` environment |

---

## 6. Exit Codes

| Exit Code | Meaning |
| :--- | :--- |
| `0` | Success (tool execution succeeded, MCP server shut down cleanly, or help/version printed) |
| `1` | Runtime, tool, registry, validation, or I/O failure (`ExecutionResult.OK == false` or server error) |
| `2` | CLI usage error (unknown command, unknown flag, invalid flag value, or missing required positional argument) |

---

## 7. Canonical Error Codes

All structured errors returned by CLI (`--json`), MCP, and `_worker` IPC use one of the canonical `contract.ErrorCode` values defined in [`internal/contract/execution.go`](../internal/contract/execution.go):

| Code | Meaning |
| :--- | :--- |
| `TOOL_NOT_FOUND` | Requested tool does not exist in scope or name is invalid |
| `VERSION_NOT_FOUND` | Requested `v<N>` directory does not exist |
| `PROPOSAL_NOT_FOUND` | Requested `proposal_id` does not exist in staging |
| `INVALID_INPUT` | Input failed `manifest.input_schema` validation |
| `INVALID_OUTPUT` | Output failed `manifest.output_schema` validation |
| `INVALID_MANIFEST` | `manifest.json` failed schema or semantic validation |
| `INVALID_TESTS` | `tests.json` failed schema or semantic validation |
| `COMPILE_ERROR` | `tool.js` failed compilation or did not define `main(input)` |
| `TEST_FAILED` | One or more mock fixture tests failed |
| `CAPABILITY_DENIED` | Script attempted an operation outside its declared capabilities |
| `NOT_FOUND` | Allowed `knurl.fs.read` target does not exist |
| `NOT_A_GIT_REPO` | `knurl.git.*` called outside a Git worktree |
| `INVALID_ARGUMENT` | `knurl.exec` parameter or HTTP header failed validation |
| `SECRET_MISSING` | Required or referenced secret is not configured on the host |
| `TIMEOUT` | Execution exceeded `timeout_ms` |
| `LIMIT_EXCEEDED` | Byte, count, heap, call-stack, or IPC frame limit exceeded |
| `TOOL_ERROR` | `tool.js` threw an unhandled JavaScript exception |
| `EXEC_FAILED` | Spawning an approved `knurl.exec` binary failed |
| `HTTP_ERROR` | Outbound HTTP request failed or was blocked by SSRF/redirect rules |
| `WORKER_CRASHED` | `_worker` subprocess exited unexpectedly or returned malformed IPC |
| `INTERNAL_ERROR` | Unexpected host error |

## 8. Human lifecycle commands

```sh
knurl approve [<proposal-id> | --last] [--yes | -y]
knurl reject [<proposal-id> | --last] [--reason "text"]
knurl rollback <tool> [--to v<N>] [--scope repo|global]
knurl proposals [--status pending|all] [--json]
```

Approval prints target, requested permission changes, elevated/escalation markers,
secret names, fixture counts, safe preview or skip reason, and source/diff. It
requires `y` or `yes` at `[y/N]`; EOF or an empty answer cancels and leaves the
proposal pending (exit 0). `--yes` is exclusively a local CLI flag. Promotion
verifies the staged digest and rejects stale bases. Repo targets are captured at
proposal time. Rollback defaults to auto resolution and restores `previous_version`
(or the explicit approved version), keeping all installed files.

`--last` is valid only for approve/reject. Listing defaults to pending and returns
compact rows: `proposal_id`, `tool`, `kind`, `scope`, `status`, `created_at`.
Rejection never updates the registry; its exact reason is returned by MCP status.
These commands do not use `KNURL_TOOLS_DIR`; approval destinations are restricted
to the repo/global registries. All commands use the global registry's config-root
precedence for staging, including `KNURL_CONFIG_DIR/staging`.

Lifecycle value flags also accept `--flag=value` (including `--reason=` for an
empty reason). Each lifecycle command supports `-h` / `--help` while parsing its
arguments and prints command-specific flags. Boolean flags do not accept values.
