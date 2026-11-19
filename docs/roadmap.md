# `knurl` v0 Roadmap

The v0 roadmap delivers a complete, usable, security-hardened tool vault across five disciplined phases, anchored by three flagship benchmark tools and an explicit Kill-or-Commit checkpoint after Phase 1.

## Flagship Benchmark Tools

Every architectural and UX choice in v0 is validated against three concrete workflows:

1. **`repo-context`** (read-only repository summary) — Validates cross-agent discovery, deterministic JSON output, and token savings without Bash access.
2. **`pr-ready`** (repo-local PR readiness checks) — Validates `./.knurl/tools/` repo scoping, multi-step Git + templated exec orchestration, and the break → repair → diff-approve → rollback lifecycle.
3. **`vps-health`** (permission-scoped ops diagnostic) — Validates standalone human CLI usage, host-injected secrets, and constrained SSH argument templates with zero shell exposure.

## Phases

### Phase 0 (Days 1–2): Foundation, Contracts & Spec Freeze *(Current)*
- **Deliverables:**
  - `mise.toml` toolchain (`go = "1.27.4"`, `golangci-lint = "2.15.0"`, `lefthook = "2.2.2"`), `.golangci.yml` v2 config, `lefthook.yml`, CI workflows, `CONTRIBUTING.md`, `AGENTS.md`.
  - Complete engineering docs (`README.md`, `docs/architecture.md`, `docs/roadmap.md`, `docs/specs/*`, 3 ADRs).
  - Draft 2020-12 JSON Schemas (`schemas/manifest.schema.json`, `schemas/tests.schema.json`).
  - Compilable Go module (`github.com/ViniTamanhao/knurl`), `cmd/knurl` stub, frozen `internal/contract` types, and pure-Go contract validation tests.
  - Contract-validated exit-gate bundles in `examples/repo-context/` and `examples/vps-health/`.
- **Exit Gate:** `mise run check` passes with zero warnings/errors; example bundles validate strictly against JSON Schemas and semantic rules.

### Phase 1 (Days 3–5): Weekend Kill-or-Commit Execution Slice
- **Deliverables:**
  - `knurl _worker` isolated subprocess with NDJSON stdin/stdout IPC, `context.WithTimeout`, and Unix process-group cleanup.
  - Synchronous `dop251/goja` runtime host bindings.
  - MVP host primitives: `os.Root`-confined `knurl.fs`, constrained `knurl.exec` templates, and env-backed host secret injection (`knurl.secrets.has`, `${secret:NAME}`).
  - Minimal `knurl run <bundle-dir|name>` CLI and minimal stdio MCP server exposing `knurl_run`.
- **Exit Gate (Kill-or-Commit):** End-to-end execution of `examples/repo-context` and `examples/vps-health` through both CLI and MCP stdio. Evaluate whether LLM-authored synchronous JS + Go host primitives deliver genuine ergonomics and reliability before investing in Phases 2–4.

### Phase 2 (Days 6–9): Registry, Full Host Surface & Adversarial Hardening
- **Deliverables:**
  - Two-tier filesystem registry (`./.knurl/tools` walking upward to `.git` + `~/.config/knurl/tools`), `active.json` atomic pointer resolution.
  - Strict JSON Schema Draft 2020-12 enforcement on tool inputs and outputs (`INVALID_INPUT` / `INVALID_OUTPUT`).
  - Full `knurl.git` (`branch`, `status`, `log`, `diff`) and `knurl.http` (`get`, `post`) host primitives with HTTPS enforcement, SSRF/private-IP blocking, and redirect secret-stripping.
  - Complete secret redaction scrubber across stdout, stderr, errors, and diagnostics (raw, base64, URL-encoded).
  - Adversarial sandbox security test suite (symlink traversal, flag/shell injection, secret exfiltration, runaway loops, child-process leaks).
- **Exit Gate:** All adversarial security tests pass under `go test -race ./...`; zero secret leakage across failure paths.

### Phase 3 (Days 10–14): Staging, Approval Gate, Rollback & Full MCP Surface
- **Deliverables:**
  - Staging store (`~/.config/knurl/staging/<proposal-id>/`) with `proposal.json` lifecycle (`pending`, `approved`, `rejected`, `superseded`) and SHA-256 bundle integrity verification.
  - Validation pipeline: schema check -> semantic validation -> `goja` compile -> mock `tests.json` runner -> safe read-only dry-run preview.
  - Capability-escalation diff engine comparing proposed version against active version.
  - One-step `knurl approve --last` / `knurl approve <proposal-id>`, `knurl reject <proposal-id>`, and `knurl rollback <name> [--to v<N>]`.
  - Complete 5-tool stdio MCP server (`knurl_discover`, `knurl_inspect`, `knurl_run`, `knurl_propose`, `knurl_status`) tested over `mcp.NewInMemoryTransports`.
- **Exit Gate:** Full Solve -> Propose -> Validate -> Approve -> Run -> Break -> Propose v2 -> Diff-Approve -> Rollback flow tested end-to-end on `pr-ready`.

### Phase 4 (Days 15–18): Polish, Benchmarks & Distribution
- **Deliverables:**
  - Companion ~30-line `SKILL.md` / agent snippet pointing agents to `knurl_discover`, `knurl_propose`, and `docs/specs/js-host-api.md`.
  - Token-count and latency benchmarks across `repo-context`, `pr-ready`, and `vps-health` comparing `knurl` vs raw shell + `SKILL.md`.
  - Human CLI polish (`knurl list`, `knurl inspect`, `knurl log`, `--json` machine output).
  - Cross-platform static release builds via GoReleaser.
- **Exit Gate:** Tagged `v0.1.0` release reproducible from clean checkout via `mise run check`.

## Explicit v0 Non-Goals

Do **not** build any of the following in v0:
- Background daemon or persistent server process
- Built-in LLM client, API key management for LLMs, or prompt orchestration inside `knurl`
- Cloud sync, remote registry, or tool marketplace
- Automatic chat transcript watching or background tool extraction
- `npm`, bundlers, TypeScript transpilation, or multi-file JS `require`/`import`
- Scheduled cron execution
- Autonomous tool repair without human approval
