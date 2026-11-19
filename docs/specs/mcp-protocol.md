# MCP Protocol Specification

`knurl mcp` serves the Model Context Protocol over **stdin/stdout** using `github.com/modelcontextprotocol/go-sdk/mcp`. Parent diagnostics write exclusively to `stderr` so JSON-RPC framing on `stdout` stays uncorrupted.

`knurl` exposes **exactly 5 static meta-tools** regardless of how many tools exist in the local registry.

---

## 1. Meta-Tool Contracts

### 1.1 `knurl_discover`

Lightweight search across approved tools. Never returns `tool.js` source code or full JSON Schemas.

**Input:**
```json
{
  "type": "object",
  "properties": {
    "query": { "type": "string", "description": "Case-insensitive substring filter over name, description, and tags" },
    "tags": { "type": "array", "items": { "type": "string" } },
    "scope": { "type": "string", "enum": ["all", "repo", "global"], "default": "all" },
    "limit": { "type": "integer", "minimum": 1, "maximum": 100, "default": 25 }
  },
  "additionalProperties": false
}
```

**Output:**
```json
{
  "tools": [
    {
      "name": "repo-context",
      "version": 1,
      "scope": "global",
      "description": "Summarize repository structure, manifests, git status, and recent changes",
      "tags": ["git", "context", "summary"],
      "read_only": true,
      "elevated": false
    }
  ]
}
```

---

### 1.2 `knurl_inspect`

Returns the complete `manifest.json` and metadata for one approved tool. Never exposes `tool.js` source unless explicitly inspected by a human via CLI (`knurl inspect --source`).

**Input:**
```json
{
  "type": "object",
  "required": ["name"],
  "properties": {
    "name": { "type": "string" },
    "scope": { "type": "string", "enum": ["auto", "repo", "global"], "default": "auto" },
    "version": { "type": "integer", "minimum": 1 }
  },
  "additionalProperties": false
}
```

**Output:**
```json
{
  "name": "repo-context",
  "version": 1,
  "scope": "global",
  "active_version": 1,
  "available_versions": [1],
  "elevated": false,
  "manifest": { "...": "full manifest.json object" }
}
```

---

### 1.3 `knurl_run`

Validates `input` against `manifest.input_schema`, executes `tool.js` inside `knurl _worker`, redacts secret values, validates output against `manifest.output_schema`, and returns a structured envelope.

**Input:**
```json
{
  "type": "object",
  "required": ["name", "input"],
  "properties": {
    "name": { "type": "string" },
    "input": { "type": "object" },
    "scope": { "type": "string", "enum": ["auto", "repo", "global"], "default": "auto" },
    "version": { "type": "integer", "minimum": 1 },
    "timeout_ms": { "type": "integer", "minimum": 100, "maximum": 120000 }
  },
  "additionalProperties": false
}
```

**Output:**
```json
{
  "ok": true,
  "tool": "repo-context",
  "version": 1,
  "scope": "global",
  "duration_ms": 42,
  "output": { "...": "validated against manifest.output_schema" },
  "diagnostics": ["optional redacted knurl.log lines"]
}
```

On failure (`ok: false`), `output` is omitted and `error` is populated:
```json
{
  "ok": false,
  "tool": "vps-health",
  "version": 1,
  "scope": "global",
  "duration_ms": 11,
  "error": {
    "code": "SECRET_MISSING",
    "message": "required secret VPS_SSH_KEY_PATH is not configured on host",
    "details": { "secret": "VPS_SSH_KEY_PATH" }
  }
}
```

---

### 1.4 `knurl_propose`

Submits a new tool (`v1`) or repair (`v<N+1>`) into `~/.config/knurl/staging/<proposal-id>/`. Runs schema validation, semantic checks, capability diffing, `goja` compilation, mock tests, and (when safe) a working-directory dry-run preview. **Never writes to active `tools/` directories or approves itself.**

**Input:**
```json
{
  "type": "object",
  "required": ["name", "scope", "summary", "manifest", "source", "tests"],
  "properties": {
    "name": { "type": "string" },
    "scope": { "type": "string", "enum": ["repo", "global"] },
    "summary": { "type": "string", "minLength": 5, "maxLength": 200 },
    "manifest": { "type": "object" },
    "source": { "type": "string", "minLength": 1 },
    "tests": { "type": "object" }
  },
  "additionalProperties": false
}
```

**Output:**
```json
{
  "proposal_id": "prop_01JQ7X9K2M...",
  "status": "pending",
  "tool": "pr-ready",
  "target_version": 2,
  "base_version": 1,
  "scope": "repo",
  "elevated": true,
  "validation": {
    "schema_valid": true,
    "compiled": true,
    "tests_passed": 3,
    "tests_total": 3,
    "failures": []
  },
  "capability_diff": {
    "elevated_before": false,
    "elevated_after": true,
    "has_escalation": true,
    "changes": [
      {
        "kind": "added",
        "capability": "exec.template",
        "detail": "Added read-only template 'git-status'"
      }
    ]
  },
  "dry_run": {
    "executed": true,
    "skipped_reason": "",
    "duration_ms": 38,
    "ok": true,
    "output_preview": { "..." : "..." }
  },
  "approval_command": "knurl approve prop_01JQ7X9K2M..."
}
```

If validation (`schema_valid`, `compiled`, or `tests_passed == tests_total`) fails, `knurl_propose` returns the validation errors so the agent can fix and re-propose—no unvalidatable proposal is left eligible for approval (`status: "rejected"` or validation failure response).

---

### 1.5 `knurl_status`

Checks the current state of a submitted proposal.

**Input:**
```json
{
  "type": "object",
  "required": ["proposal_id"],
  "properties": {
    "proposal_id": { "type": "string", "minLength": 1 }
  },
  "additionalProperties": false
}
```

**Output:**
```json
{
  "proposal_id": "prop_01JQ7X9K2M...",
  "status": "approved",
  "tool": "pr-ready",
  "target_version": 2,
  "base_version": 1,
  "scope": "repo",
  "created_at": "2026-11-19T12:00:00Z",
  "decided_at": "2026-11-19T12:01:15Z",
  "decision_note": "approved via CLI",
  "approval_command": ""
}
```

---

## 2. Canonical Error Codes

All `knurl` runtime, validation, and MCP execution envelopes use the following canonical `ErrorCode` values (`internal/contract/execution.go`):

| Code | Meaning |
| :--- | :--- |
| `TOOL_NOT_FOUND` | Requested tool name does not exist in target scope |
| `VERSION_NOT_FOUND` | Requested tool version `v<N>` does not exist |
| `PROPOSAL_NOT_FOUND` | Requested `proposal_id` does not exist in staging |
| `INVALID_INPUT` | Caller input failed `manifest.input_schema` validation |
| `INVALID_OUTPUT` | Tool return value failed `manifest.output_schema` validation |
| `INVALID_MANIFEST` | `manifest.json` failed JSON Schema or semantic validation |
| `INVALID_TESTS` | `tests.json` failed JSON Schema or semantic validation |
| `COMPILE_ERROR` | `tool.js` failed `goja` compilation or lacked `main(input)` |
| `TEST_FAILED` | One or more fixture cases in `tests.json` failed |
| `CAPABILITY_DENIED` | Script accessed an undeclared path, domain, template, git API, or secret |
| `NOT_FOUND` | Allowed `fs.read` target file does not exist |
| `NOT_A_GIT_REPO` | `knurl.git.*` called outside a Git working tree |
| `INVALID_ARGUMENT` | `knurl.exec` argument failed `args_schema` regex, length, or enum check |
| `SECRET_MISSING` | Referenced secret is not configured on the host |
| `TIMEOUT` | Execution exceeded `timeout_ms` and was terminated |
| `LIMIT_EXCEEDED` | File read, glob result, HTTP body, or IPC payload exceeded byte/count cap |
| `TOOL_ERROR` | Tool script threw an unhandled JS exception (`throw new Error(...)`) |
| `EXEC_FAILED` | Host failed to spawn template binary (missing binary, OS exec error) |
| `HTTP_ERROR` | Network failure, TLS error, blocked SSRF target, or disallowed redirect |
| `WORKER_CRASHED` | Isolated `_worker` subprocess exited unexpectedly without a valid frame |
| `INTERNAL_ERROR` | Unexpected host failure |
