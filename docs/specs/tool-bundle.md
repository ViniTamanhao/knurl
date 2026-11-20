# Tool Bundle Specification

This specification defines the on-disk directory structure, versioning rules, precedence, and staging layout for `knurl` tool bundles.

## 1. Tool Naming & Version Numbers

- **Tool name:** `^[a-z][a-z0-9]*(-[a-z0-9]+)*$`, length 2–64 characters (e.g., `repo-context`, `pr-ready`, `vps-health`). Invalid, empty, or path-traversal names (`../evil`, `/abs`, `a/b`) fail closed with `TOOL_NOT_FOUND` before touching the filesystem.
- **Version directory:** `v<N>` where `<N>` is a monotonically increasing positive integer (`v1`, `v2`, ...). Published version directories are immutable real directories (never symlinks); `manifest.version` must match `<N>` (`INVALID_MANIFEST` on mismatch); repairs always increment `N`.

## 2. Active Registry Directory Layout

Both tiers share an identical directory structure under `<tier-root>/tools/`:

```text
tools/
  <name>/
    active.json
    v1/
      manifest.json
      tool.js
      tests.json
      approval.json
    v2/
      manifest.json
      tool.js
      tests.json
      approval.json
```

Flat unversioned bundle directories (containing `manifest.json` and `tool.js` directly, such as `examples/repo-context/` or direct path invocations) are also supported for local development and `--tools-dir` overrides.

### Bundle Files

| File | Authored By | Mutability | Description |
| :--- | :--- | :--- | :--- |
| `manifest.json` | Agent / Human | Immutable once approved | Typed metadata, JSON Schemas, capabilities, secret refs (`schemas/manifest.schema.json`). |
| `tool.js` | Agent / Human | Immutable once approved | Synchronous ES6 JS defining `function main(input)`. |
| `tests.json` | Agent / Human | Immutable once approved | Mock-backed deterministic contract tests (`schemas/tests.schema.json`). |
| `approval.json` | `knurl` Host | Immutable once approved | Host-written audit record stamped during `knurl approve`. |
| `active.json` | `knurl` Host | Atomically updated | Active and previous version pointers for fast lookup and rollback. |

### `active.json` Format

```json
{
  "active_version": 2,
  "previous_version": 1,
  "updated_at": "2026-11-19T12:00:00Z"
}
```

- `previous_version` is `0` (or omitted) when only one version has been approved.
- `active.json` MUST be updated via write-to-temp-file in the same directory, `fsync`, and atomic `os.Rename` (no symlinks; portable across Windows and Unix).
- When a tool directory contains versioned `v<N>/` subdirectories without an `active.json` file, the highest valid `v<N>` directory is selected as the active version. When `active.json` exists, `active_version` (`>= 1`) selects the active directory directly (failing closed with `INVALID_MANIFEST` if malformed or `VERSION_NOT_FOUND` if the target `v<N>/` is missing).

### `approval.json` Format

```json
{
  "proposal_id": "prop_0123456789abcdef0123456789abcdef",
  "approved_at": "2026-11-19T12:00:00Z",
  "bundle_sha256": "e3b0c44298fc1c149afbf4c8996fb924...",
  "summary": "Add branch base flag fallback",
  "capability_diff": {
    "elevated_before": false,
    "elevated_after": false,
    "has_escalation": false,
    "changes": []
  }
}
```

`bundle_sha256` is the canonical SHA-256 digest over the exact staged bytes of `manifest.json`, `tool.js`, and `tests.json`.

## 3. Tier Resolution & Precedence

Base paths:
- **Repo-local tier:** `./.knurl/tools/` discovered by walking upward from `workdir` (`cwd`) until the first directory containing `.knurl/tools/` or a `.git` repository boundary (supporting both `.git` directories and `.git` worktree/submodule files, stopping at `.git` or the filesystem root).
- **Global tier:** resolved in order:
  1. `$KNURL_HOME/tools/` (when `KNURL_HOME` is non-empty)
  2. `$KNURL_CONFIG_DIR/tools/` (when `KNURL_CONFIG_DIR` is non-empty)
  3. `$XDG_CONFIG_HOME/knurl/tools/` (when `XDG_CONFIG_HOME` is non-empty)
  4. `~/.config/knurl/tools/`
- **Override tier (`override`):** explicit `--tools-dir <dir>` (`KNURL_TOOLS_DIR`) or direct bundle directory paths (`knurl run ./path/to/bundle`). When an override directory is configured, `auto` lookup resolves strictly inside that override directory without falling back to the user's global tier.

Lookup behavior for `scope`:
- `auto` (default for `run` and `inspect`): search **repo-local** first; if the tool directory is absent, fall back to **global**. Repo-local tools shadow global tools of the same name (malformed repo-local bundles fail closed without falling back to global).
- `repo`: search repo-local only.
- `global`: search global only.
- `override`: search configured override directory only.
- `all` for MCP discovery: search both tiers with repo-local precedence on name
  collisions. The lower-level `List(all)` inventory retains both scope labels.

## 4. Rollback Semantics

`knurl rollback <name> [--to v<N>] [--scope repo|global]`:
- Without `--to`, switches `active.json` to `previous_version` (and records the outgoing active version as the new `previous_version`).
- With `--to v<N>`, verifies `v<N>/` exists with valid `approval.json` and updates `active.json` atomically (`active_version: N`, `previous_version: <outgoing>`).
- Rollback never deletes version directories, preserving full auditability and forward/backward switching.

## 5. Staging Directory Layout

Unapproved proposals live strictly outside active tool directories under `<global-root>/staging/<proposal-id>/` (even when targeting `scope: "repo"`):

```text
staging/
  <proposal-id>/
    proposal.json
    manifest.json
    tool.js
    tests.json
```

`proposal.json` stores `proposal_id`, `tool_name`, `target_version`, `base_version`, `scope`, `summary`, `status` (`pending` | `approved` | `rejected` | `superseded`), `bundle_sha256`, `elevated`, `validation`, `capability_diff`, `dry_run` (if run), `created_at`, `decided_at`, and `decision_note`.

When a proposal is approved, any older `pending` proposals for the same `(scope, tool_name)` may be marked `superseded` to keep `knurl approve --last` unambiguous.

## Phase 3 implementation contract

The pointer remains `active.json` (not a symlink or `current` file). Promotion
writes and syncs a complete version before switching the pointer. Registry
mutations serialize with an exclusive lock directory; stale locks fail closed.
Repair requests may include `base_version`; when omitted it is inferred as
`manifest.version - 1`. Both submission and approval reject stale bases.

The proposal record additionally stores `kind` (`create` or `repair`),
`target_repo_root` (absolute repo destination for repo proposals), `source_diff`
(unified tool.js diff), and `promoted_version` after approval. `scope` is the
canonical target scope field; `decided_at` is the approval/rejection timestamp.
Validation includes per-case `tests` entries (`name`, `passed`, optional `error`).
The digest hashes each filename, NUL, byte length, NUL, and exact file bytes in
manifest.json, tool.js, tests.json order. Invalid submissions are not persisted.

### Exact `proposal.json` fields

Unknown optional metadata is not an executable capability. The canonical Go
record is `contract.ProposalRecord`; JSON timestamps are UTC RFC 3339.

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `proposal_id` | string | `prop_` followed by 32 lowercase hex characters from 128 random bits |
| `tool_name` | string | Validated bundle name |
| `kind` | string | `create` when base is zero; otherwise `repair` |
| `target_version`, `base_version` | integer | Next installed version and reviewed active base (zero for creation) |
| `scope` | string | `repo` or `global` |
| `target_repo_root` | optional string | Absolute destination captured at proposal time; approval works from another terminal directory |
| `summary` | string | Agent's 5–200 character review summary |
| `status` | string | `pending`, `approved`, `rejected`; `superseded` reserved by the frozen contract |
| `bundle_sha256` | string | Lowercase 64-character hex digest of exact bundle bytes |
| `elevated` | boolean | Derived from the manifest, including HTTP and secrets |
| `validation` | object | `schema_valid`, `compiled`, `tests_passed`, `tests_total`, `failures` (string array), `tests` (per-case array) |
| `validation.tests[]` | object | `name`, `passed`, optional structured `error` (`code`, `message`, optional `details`), and redacted `output` on an expectation mismatch |
| `capability_diff` | object | `elevated_before`, `elevated_after`, `has_escalation`, `changes` |
| `capability_diff.changes[]` | object | `kind` (`added`, `removed`, `unchanged`), `capability`, `detail` |
| `source_diff` | optional string | Unified whole-file hunk for repairs; omitted when identical |
| `dry_run` | optional object | `executed`, `ok`, optional `skipped_reason`, `duration_ms`, `output_preview`, `error` |
| `created_at` | timestamp | Successful validation and staging time |
| `decided_at` | optional timestamp | Human approval or rejection time |
| `decision_note` | optional string | `approved via CLI` or exact human rejection reason |
| `promoted_version` | optional integer | Installed version, populated after approval |

`approve --last` selects the newest pending record, breaking timestamp ties by
proposal ID. Rejected records remain auditable but are never eligible. Competing
pending proposals remain pending; approval rechecks their base and rejects stale
ones. Version allocation uses the highest installed version plus one, even after
rollback; use explicit `base_version` when the active version differs from the
highest installed version. `previous_version` permits repeated backward/forward
rollback. Rollback validates the target bundle and requires its approval record.

Proposal listing and `approve --last` skip unreadable or malformed staging records;
those records remain on disk and direct ID inspection still reports the error.
Atomic metadata and installation temporary names use 128 random bits plus exclusive
creation, avoiding dependence on clock resolution.
