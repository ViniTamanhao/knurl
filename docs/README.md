# `knurl` Technical Documentation

This directory contains the engineering architecture, CLI reference, protocol and security specifications, Architecture Decision Records (ADRs), and phased implementation plan for `knurl`.

---

## Architecture & Reference

| Document | Scope |
| :--- | :--- |
| [`architecture.md`](architecture.md) | Component boundaries, acyclic dependency rules, `_worker` subprocess NDJSON IPC, resource limits, two-tier registry lookup, and approval sequence |
| [`cli-reference.md`](cli-reference.md) | CLI subcommands (`run`, `mcp`, `version`, `_worker`), flags, environment variable precedence, exit codes, `ExecutionResult` envelope, and canonical error codes |
| [`roadmap.md`](roadmap.md) | Phased v0 implementation plan (Phases 0–4), flagship benchmark workflows, exit gates, and explicit non-goals |

---

## Specifications (`docs/specs/`)

Normative contracts synchronized with [`schemas/`](../schemas/) and [`internal/contract/`](../internal/contract/):

| Specification | Scope |
| :--- | :--- |
| [`specs/tool-bundle.md`](specs/tool-bundle.md) | On-disk bundle layout (`manifest.json`, `tool.js`, `tests.json`, `approval.json`, `active.json`), naming/versioning rules, two-tier precedence, and staging format |
| [`specs/js-host-api.md`](specs/js-host-api.md) | Concise synchronous ES6 JavaScript host API (`knurl.fs`, `knurl.git`, `knurl.http`, `knurl.exec`, `knurl.secrets`, `knurl.log`), limits, and unsupported globals |
| [`specs/mcp-protocol.md`](specs/mcp-protocol.md) | Input/output schemas and error codes for the 5 static MCP meta-tools over stdio (`knurl_discover`, `knurl_inspect`, `knurl_run`, `knurl_propose`, `knurl_status`) |
| [`specs/security-model.md`](specs/security-model.md) | Threat model, derived `elevated` flag, capability-escalation diffing, `os.Root` confinement, RE2-anchored `exec` templates, SSRF-safe `http`, secret redaction, and worker limits |

---

## Architecture Decision Records (`docs/adr/`)

| ADR | Decision |
| :--- | :--- |
| [`adr/0001-synchronous-goja-runtime.md`](adr/0001-synchronous-goja-runtime.md) | Embedding synchronous `goja` ES6 JavaScript over Lua, Starlark, WebAssembly, or async Node runtimes |
| [`adr/0002-static-mcp-meta-tools.md`](adr/0002-static-mcp-meta-tools.md) | Exposing 5 static MCP meta-tools over dynamic `tools/list` registration |
| [`adr/0003-two-tier-registry-and-staging.md`](adr/0003-two-tier-registry-and-staging.md) | Repo-local + global two-tier scoping, atomic `active.json` pointers, and mandatory staging |

---

## Codebase Package Map

`knurl` enforces an acyclic internal dependency graph rooted at `internal/contract`:

| Package | Responsibility |
| :--- | :--- |
| [`cmd/knurl`](../cmd/knurl/) | CLI entrypoint, argument parsing, human/JSON formatting, `knurl mcp` startup, and hidden `_worker` dispatch |
| [`internal/contract`](../internal/contract/) | Frozen JSON-serializable manifest/test/IPC/MCP structs, canonical error codes, and Draft 2020-12 schema/semantic validation |
| [`internal/registry`](../internal/registry/) | Two-tier repo-local (`./.knurl/tools`) and global (`~/.config/knurl/tools`) discovery, `active.json` resolution, and versioned bundle loading |
| [`internal/host`](../internal/host/) | Capability-enforced `os.Root` filesystem, hardened read-only Git, argv-only templated `exec`, SSRF/redirect-safe `http`, and host secret injection/redaction |
| [`internal/runtime`](../internal/runtime/) | Parent `Supervisor`, 2 MiB NDJSON `_worker` subprocess IPC, Unix rlimits/heap watchdog, `-pgid` teardown, and synchronous `goja` VM bindings |
| [`internal/approval`](../internal/approval/) | Staging store, validation pipeline, capability-escalation diffing, atomic promotion, and rollback |
| [`internal/mcpserver`](../internal/mcpserver/) | Stdio MCP server wiring static meta-tools via `github.com/modelcontextprotocol/go-sdk/mcp` |

---

## JSON Schemas & Reference Bundles

- [`schemas/manifest.schema.json`](../schemas/manifest.schema.json) — JSON Schema (Draft 2020-12) for `manifest.json` (embedded via [`schemas/embed.go`](../schemas/embed.go)).
- [`schemas/tests.schema.json`](../schemas/tests.schema.json) — JSON Schema (Draft 2020-12) for `tests.json`.
- [`examples/repo-context/`](../examples/repo-context/) — Read-only `knurl.fs` + `knurl.git` reference bundle.
- [`examples/vps-health/`](../examples/vps-health/) — Constrained `knurl.exec` + `knurl.http` + host-injected `secrets` reference bundle.
