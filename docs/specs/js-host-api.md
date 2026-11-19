# `knurl` JavaScript Host API (`v1`)

Tools execute inside an isolated **synchronous ES6 JavaScript (`goja`)** runtime. Every `tool.js` must define `function main(input)` and return a JSON-serializable object matching `manifest.json`'s `output_schema`.

```javascript
function main(input) {
  var files = knurl.fs.glob("**/*.go", { max_results: 200 });
  var branch = knurl.git.branch();
  return { branch: branch.current, go_files: files.length };
}
```

## 1. Filesystem (`knurl.fs`)

All paths are relative to the working directory and confined by `os.Root` + `capabilities.fs.read`. Leading `/` and `..` traversal throw `CAPABILITY_DENIED`.

- **`knurl.fs.read(path, opts?) -> string`**
  Reads a UTF-8 text file. `opts`: `{ max_bytes?: number, truncate?: boolean }`. Defaults: `max_bytes = 262144` (max `1048576`), `truncate = false`. Throws `NOT_FOUND`, `CAPABILITY_DENIED`, or `LIMIT_EXCEEDED` (if size exceeds `max_bytes` and `truncate` is `false`).
- **`knurl.fs.glob(pattern, opts?) -> string[]`**
  Returns sorted `/`-normalized matching file paths. `opts`: `{ max_results?: number, include_hidden?: boolean, ignore?: string[] }`. Defaults: `max_results = 500` (max `5000`), `include_hidden = false`.
- **`knurl.fs.stat(path) -> { exists, is_dir, size, mod_time }`**
  Inspects a path (`mod_time` is RFC 3339 UTC). Returns `exists: false` (`is_dir: false, size: 0, mod_time: ""`) when policy allows the path and it is missing; throws `CAPABILITY_DENIED` if disallowed.

## 2. Git (`knurl.git`) — Read-Only

Requires `capabilities.git.read: true`. Throws `NOT_A_GIT_REPO` outside a Git worktree.

- **`knurl.git.branch() -> { current, detached, head, upstream }`** (`upstream` is `""` if unset)
- **`knurl.git.status() -> { clean, staged, unstaged, untracked }`** (`staged`/`unstaged`: `Array<{ path, status }>` with single-letter codes `M|A|D|R|C|U`, `untracked`: `string[]`)
- **`knurl.git.log(opts?) -> Array<{ hash, short_hash, author, date, subject }>`** (`date` is RFC 3339 UTC; `opts`: `{ limit?: number, ref?: string, path?: string }`; default `limit = 20`, max `100`)
- **`knurl.git.diff(opts?) -> { files, patch?, truncated }`** (`opts`: `{ base?: string, head?: string, staged?: boolean, stat_only?: boolean, path?: string }`; `files`: `Array<{ path, additions, deletions, binary }>`; `patch` omitted when `stat_only: true`)

## 3. HTTP (`knurl.http`)

Requires matching rule in `capabilities.http.allow` (HTTPS required unless `allow_http: true`). No redirects across domains; secret headers stripped on redirect.

- **`knurl.http.get(url, opts?) -> { status, headers, body, truncated }`** (`headers` has lowercased keys)
- **`knurl.http.post(url, body, opts?) -> { status, headers, body, truncated }`** (`body`: string or JSON-serializable value; objects auto-encode as `application/json`)
- `opts`: `{ headers?: { [key: string]: string }, timeout_ms?: number }` (default `timeout_ms = 10000`). Header values may include `${secret:NAME}` when `NAME` is listed in that rule's `secrets` (single-pass host substitution).

## 4. Constrained Exec (`knurl.exec`)

- **`knurl.exec(templateId, params?, opts?) -> { exit_code, stdout, stderr, truncated, duration_ms }`**
  Executes a template declared in `capabilities.exec.templates` directly via argv (no shell). `params` is `{ [key: string]: string }` validated against `args_schema`. `opts`: `{ timeout_ms?: number }`.
  **Non-zero exit codes return normally**; throws only on `CAPABILITY_DENIED`, `INVALID_ARGUMENT`, `SECRET_MISSING`, `TIMEOUT`, or `EXEC_FAILED`.

## 5. Secrets & Diagnostics

- **`knurl.secrets.has(name) -> boolean`** — Returns `true` if declared secret `name` is configured on the host (never reveals values; throws `CAPABILITY_DENIED` if undeclared).
- **`knurl.log(message)`** — Appends a redacted diagnostic string to the execution trace (never emitted to tool JSON output).

## 6. Errors, Limits & Unsupported Features

- **Host errors** are `Error` instances with `.code`: `CAPABILITY_DENIED`, `NOT_FOUND`, `NOT_A_GIT_REPO`, `INVALID_ARGUMENT`, `SECRET_MISSING`, `TIMEOUT`, `LIMIT_EXCEEDED`, `EXEC_FAILED`, `HTTP_ERROR`. Any uncaught `throw` becomes `TOOL_ERROR`.
- **Default limits:** Execution timeout `30000 ms` (max `120000 ms`), `fs.read` `256 KiB` (`1 MiB` max), `http` body `1 MiB`, `exec` stdout/stderr `256 KiB` each, `glob` `500` results (`5000` max).
- **Unsupported (do NOT use):** `async`/`await`, `Promise`, `setTimeout`/`setInterval`, `require`/`import`, `fetch`, `XMLHttpRequest`, `process`, `Buffer`, `console`, `__dirname`, filesystem writes.
