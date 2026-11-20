# Implementation Plan & Roadmap (v0)

This document is the single source of truth for `knurl`'s phased v0 delivery plan. Each phase has concrete deliverables, verifiable exit gates, and explicit scope boundaries so execution stays disciplined and incremental.

---

## Phase Overview

| Phase | Focus | Status | Primary Exit Gate |
| :--- | :--- | :--- | :--- |
| **Phase 0** | Foundation, Contracts & Spec Freeze | Completed | `mise run check` passes; schemas and example bundles validate |
| **Phase 1** | Core Execution Slice (Kill-or-Commit) | Completed | End-to-end CLI & MCP execution of `repo-context` and `vps-health` |
| **Phase 2** | Two-Tier Registry, `knurl.http` & Hardening | Completed | 10-category adversarial security suite passes under `-race`; zero secret leaks |
| **Phase 3** | Staging, Approval Gate, Rollback & Full MCP | Planned | End-to-end propose -> approve -> run -> repair -> diff-approve -> rollback on `pr-ready` |
| **Phase 4** | Polish, Benchmarks & Distribution | Planned | Reproducible `v0.1.0` static release builds and benchmark report |

---

## Flagship Benchmark Tools

Every architectural, security, and UX decision in v0 is validated against three concrete workflows:

| Tool | Target Scope | Capabilities Exercised | What It Validates |
| :--- | :--- | :--- | :--- |
| **`repo-context`** ([`examples/repo-context/`](../examples/repo-context/)) | Global / Override | `fs.read`, `git.read` (`read_only: true`) | Cross-agent discovery, deterministic JSON output, and token savings with zero shell access |
| **`pr-ready`** | Repo-local (`./.knurl/tools/`) | `fs.read`, `git.read`, templated `exec` | Two-tier precedence and the break -> propose `v2` -> diff-approve -> rollback lifecycle |
| **`vps-health`** ([`examples/vps-health/`](../examples/vps-health/)) | Global / Override | Templated `exec` (SSH), `http.allow`, `secrets` | Standalone human CLI utility, host-injected secrets, redaction, and constrained SSH templates |

---

## Detailed Phases

### Phase 0: Foundation, Contracts & Spec Freeze *(Completed)*

- **Deliverables:**
  - `mise.toml` toolchain (`go = "1.27.4"`, `golangci-lint = "2.15.0"`, `lefthook = "2.2.2"`), `.golangci.yml` v2 config, `lefthook.yml`, CI workflows, `CONTRIBUTING.md`, `AGENTS.md`.
  - Technical specifications (`docs/architecture.md`, `docs/specs/*`, 3 ADRs) and roadmap.
  - Draft 2020-12 JSON Schemas (`schemas/manifest.schema.json`, `schemas/tests.schema.json`).
  - Compilable Go module (`github.com/ViniTamanhao/knurl`), `cmd/knurl` entrypoint, frozen `internal/contract` types, and pure-Go contract validation tests.
  - Contract-validated reference bundles in `examples/repo-context/` and `examples/vps-health/`.
- **Exit Gate:** `mise run check` passes with zero warnings or errors; example bundles validate strictly against JSON Schemas and semantic rules.

### Phase 1: Core Execution Slice — Kill-or-Commit *(Completed)*

- **Deliverables:**
  - `knurl _worker` isolated subprocess with 2 MiB NDJSON stdin/stdout IPC, sanitized worker environment, `context.WithTimeout`, `goja` interrupt watchdog, and Unix `-pgid` process-group cleanup.
  - Synchronous `dop251/goja` runtime host bindings (`internal/runtime`) with sandbox hygiene checks and canonical error propagation.
  - Capability-enforced host primitives (`internal/host`): `os.Root`-confined `knurl.fs`, read-only `knurl.git`, constrained argv-only `knurl.exec` templates, host secret injection (`knurl.secrets.has`, `${secret:NAME}`), and secret redaction.
  - Runtime Draft 2020-12 input/output JSON Schema validation (`INVALID_INPUT` / `INVALID_OUTPUT`), initial bundle loader (`internal/registry`), `knurl run <tool-name-or-path>` CLI, and initial stdio MCP server (`knurl mcp`) exposing `knurl_run`.
- **Exit Gate (Kill-or-Commit):** End-to-end execution of `examples/repo-context` and `examples/vps-health` through both CLI and MCP stdio.

### Phase 2: Two-Tier Registry, `knurl.http` & Adversarial Hardening *(Completed)*

- **Deliverables:**
  - Two-tier filesystem registry (`./.knurl/tools` walking upward to `.git` dir/worktree file + `$KNURL_HOME` / `$KNURL_CONFIG_DIR` / `$XDG_CONFIG_HOME` / `~/.config/knurl/tools`), `active.json` pointer and latest `v<N>` resolution, override (`--tools-dir` / direct path) support, `--scope` / `--version` CLI flags, and `Resolve` / `LoadVersion` / `List` APIs wired into `knurl run` and `knurl mcp`.
  - Complete domain-allowlisted synchronous `knurl.http` (`get`, `post`) with HTTPS enforcement, SSRF literal/DNS-CIDR blocking + direct-IP dialing, boundary-aware `path_prefixes`, `CheckRedirect` blocking cross-domain / HTTPS->HTTP / disallowed hops, redirect secret-header stripping, and 1 MiB body caps.
  - Hardened `knurl.fs`, `knurl.git`, and `knurl.exec` (disjoint glob pre-checks, literal glob prefix symlink checks, `--no-ext-diff` / `--no-textconv` / `--` git hardening, `\A(?:...)\z` RE2 anchoring, dynamic-linker env blocklist), `_worker` memory/stack/rlimit safeguards (`RLIMIT_CORE=0`, `RLIMIT_NOFILE<=256`, `debug.SetMemoryLimit`, active heap watchdog, `SetMaxCallStackSize`), graceful-then-forced `-pgid` cleanup, and multi-line/JSON-safe secret redaction.
  - Automated 10-category adversarial security & sandbox test suite (`internal/host/security_test.go`, `internal/runtime/adversarial_test.go`) plus live TLS `httptest.Server` integration for `examples/vps-health`.
- **Exit Gate:** All adversarial security tests pass under `go test -race ./...`; zero secret leakage across failure paths.

### Phase 3: Staging, Approval Gate, Rollback & Full MCP Surface *(Planned)*

- **Deliverables:**
  - Staging store (`~/.config/knurl/staging/<proposal-id>/`) with `proposal.json` lifecycle (`pending`, `approved`, `rejected`, `superseded`) and SHA-256 bundle integrity verification.
  - Validation pipeline: schema check -> semantic validation -> `goja` compile -> mock `tests.json` runner -> safe read-only dry-run preview.
  - Capability-escalation diff engine comparing proposed version against active version (`elevated_before`, `elevated_after`, `has_escalation`, `changes`).
  - Human approval and rollback CLI commands: `knurl approve --last` / `knurl approve <proposal-id>`, `knurl reject <proposal-id>`, and `knurl rollback <name> [--to v<N>]`.
  - Complete 5-tool stdio MCP server (`knurl_discover`, `knurl_inspect`, `knurl_run`, `knurl_propose`, `knurl_status`) tested over `mcp.NewInMemoryTransports`.
- **Exit Gate:** Full Solve -> Propose -> Validate -> Approve -> Run -> Break -> Propose `v2` -> Diff-Approve -> Rollback flow tested end-to-end on `pr-ready`.

### Phase 4: Polish, Benchmarks & Distribution *(Planned)*

- **Deliverables:**
  - Companion ~30-line `SKILL.md` / agent snippet pointing agents to `knurl_discover`, `knurl_propose`, and [`docs/specs/js-host-api.md`](specs/js-host-api.md).
  - Token-count and latency benchmarks across `repo-context`, `pr-ready`, and `vps-health` comparing `knurl` against raw shell + `SKILL.md`.
  - Human CLI polish (`knurl list`, `knurl inspect`, `knurl log`, `--json` machine output).
  - Cross-platform static release builds via GoReleaser.
- **Exit Gate:** Tagged `v0.1.0` release reproducible from a clean checkout via `mise run check`.

---

## Explicit v0 Non-Goals

Do **not** build any of the following in v0:

- Background daemon or persistent server process
- Built-in LLM client, API key management for LLMs, or prompt orchestration inside `knurl`
- Cloud sync, remote registry, or tool marketplace
- Automatic chat transcript watching or background tool extraction
- `npm`, bundlers, TypeScript transpilation, or multi-file JS `require`/`import`
- Scheduled cron execution
- Autonomous tool repair without human approval
