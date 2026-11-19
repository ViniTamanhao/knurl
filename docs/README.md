# `knurl` Documentation Index

This directory holds the frozen v0 architecture, roadmap, specifications, and Architecture Decision Records (ADRs) for `knurl`.

## Core Design & Planning

- [`architecture.md`](architecture.md) — System boundaries, single-binary control plane, `_worker` subprocess IPC isolation, two-tier registry lookup, and package layout.
- [`roadmap.md`](roadmap.md) — Five-phase v0 roadmap (Phases 0–4), Kill-or-Commit gate, flagship benchmark tools, and explicit v0 non-goals.

## Frozen Specifications (`docs/specs/`)

- [`specs/tool-bundle.md`](specs/tool-bundle.md) — On-disk bundle layout (`manifest.json`, `tool.js`, `tests.json`, `approval.json`, `active.json`), versioning, rollback, precedence, and staging layout.
- [`specs/js-host-api.md`](specs/js-host-api.md) — Concise (~1-page) LLM-facing specification for authoring synchronous `tool.js` modules (`knurl.fs`, `knurl.git`, `knurl.http`, `knurl.exec`, `knurl.secrets`, `knurl.log`).
- [`specs/mcp-protocol.md`](specs/mcp-protocol.md) — Exact parameter schemas, output shapes, and canonical error codes for the 5 static MCP meta-tools over stdio.
- [`specs/security-model.md`](specs/security-model.md) — Threat model, approve-once trust boundary, capability-escalation diffing, `os.Root` confinement, constrained `exec` templates, host-only secret injection/redaction, and worker limits.

## Architecture Decision Records (`docs/adr/`)

- [`adr/0001-synchronous-goja-runtime.md`](adr/0001-synchronous-goja-runtime.md) — Why synchronous `goja` JavaScript over Lua, Starlark, and WebAssembly.
- [`adr/0002-static-mcp-meta-tools.md`](adr/0002-static-mcp-meta-tools.md) — Why 5 static MCP meta-tools over dynamic MCP `tools/list` registration.
- [`adr/0003-two-tier-registry-and-staging.md`](adr/0003-two-tier-registry-and-staging.md) — Why repo-local + global scoping, explicit `active.json` pointers, and mandatory staging.

## Schemas & Reference Bundles

- [`../schemas/manifest.schema.json`](../schemas/manifest.schema.json) — JSON Schema (Draft 2020-12) for `manifest.json`.
- [`../schemas/tests.schema.json`](../schemas/tests.schema.json) — JSON Schema (Draft 2020-12) for `tests.json`.
- [`../examples/repo-context/`](../examples/repo-context/) — Read-only `fs` + `git` flagship example bundle.
- [`../examples/vps-health/`](../examples/vps-health/) — Constrained `exec` + `http` + host-injected `secrets` flagship example bundle.
