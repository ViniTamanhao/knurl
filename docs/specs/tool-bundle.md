# Tool Bundle Specification

This specification defines the on-disk directory structure, versioning rules, precedence, and staging layout for `knurl` tool bundles.

## 1. Tool Naming & Version Numbers

- **Tool name:** `^[a-z][a-z0-9]*(-[a-z0-9]+)*$`, length 2–64 characters (e.g., `repo-context`, `pr-ready`, `vps-health`).
- **Version directory:** `v<N>` where `<N>` is a monotonically increasing positive integer (`v1`, `v2`, ...). Published version directories are immutable; repairs always increment `N`.

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

### `approval.json` Format

```json
{
  "proposal_id": "prop_01JQ7X9K2M...",
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
- **Repo-local tier:** `./.knurl/tools/` discovered by walking upward from `cwd` until the first directory containing `.knurl/tools/` or a `.git` repository root (stopping at the `.git` boundary or filesystem root).
- **Global tier:** `$KNURL_HOME/tools/` if `KNURL_HOME` is set; otherwise `$XDG_CONFIG_HOME/knurl/tools/` (default `~/.config/knurl/tools/`).

Lookup behavior for `scope`:
- `auto` (default for `run` and `inspect`): search **repo-local** first; if absent, fall back to **global**. Repo-local tools shadow global tools of the same name.
- `repo`: search repo-local only.
- `global`: search global only.
- `all` (for `discover`): list tools from both tiers with explicit `scope` labels (`repo` and `global`).

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
