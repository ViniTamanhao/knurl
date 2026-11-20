# AGENTS.md — Operational Guide for AI Coding Agents

This file defines the product vision, engineering philosophy, Git workflow, and non-negotiable invariants for AI coding agents contributing to **`knurl`**.

---

## 1. Core Idea

The name **knurl** comes from machining: cutting a textured grip into smooth metal so a human hand can reliably hold and turn a tool.

`knurl` applies that principle to AI-assisted engineering. It is a **single-binary, local-first Go tool vault** that turns ephemeral, one-off scripts written by AI agents during a session into **durable, typed, capability-scoped synchronous JavaScript tools** (`tool.js` + `manifest.json` + `tests.json`) that both humans and agents can reuse permanently across terminals (CLI) and MCP-capable clients (stdio MCP)—without granting raw shell access.

---

## 2. Philosophy

Every design and implementation choice in this repository follows six core principles:

1. **Agents write the code; humans hold the grip.** Agents author, test, and propose repairs to `tool.js`; humans inspect capability diffs, approve once per version, and own clean, immutable files on disk.
2. **Local-first, stateless, zero runtime bloat.** No embedded LLM, no background daemon, no cloud sync, and no external language runtimes (Node.js, Python, Deno). One static Go binary runs identically everywhere.
3. **Dual-use by design.** Every approved tool is first-class in both human terminals (`knurl run <tool>`) and agent sessions (`knurl mcp`), including clients that have zero Bash/shell access.
4. **Least-privilege boundaries enforced in Go.** Tool JavaScript is treated as untrusted until reviewed, and even approved tools run under strict host-enforced capabilities (`os.Root` filesystem confinement, read-only Git, argv-only templated `exec`, SSRF-safe allowlisted `http`, host-only secret injection and redaction).
5. **Non-bypassable human approval.** Agents can never write directly to active `tools/` directories or self-approve capabilities. Every creation or version upgrade flows through staging, automated validation, capability-escalation diffing, and explicit human approval.
6. **Contracts before code.** Specifications (`docs/specs/`), JSON Schemas (`schemas/`), Go contract types (`internal/contract/`), and reference bundles (`examples/`) are the source of truth and must stay strictly synchronized.

---

## 3. Goal Product

The v0 goal product is a production-grade, cross-platform static `knurl` binary (`v0.1.0`) delivering the complete **4-step tool lifecycle**:

1. **Solve** a real workflow with an agent.
2. **Propose** a typed tool bundle (`manifest.json`, synchronous `tool.js`, `tests.json`) to staging via `knurl_propose`.
3. **Validate & Approve** via automated schema/compile/mock-test checks, safe read-only dry-run, capability-escalation diffing, and explicit human `knurl approve`.
4. **Reuse, Discover, Repair & Rollback** across sessions and clients via `knurl run`, the 5 static MCP meta-tools, versioned upgrades (`v2`), and atomic `knurl rollback`.

Everything in v0 is anchored by three flagship workflows—**`repo-context`** (read-only repo summary), **`pr-ready`** (repo-local PR readiness checks & repair/rollback lifecycle), and **`vps-health`** (permission-scoped ops diagnostic with templated SSH and host-injected secrets)—and tracked phase-by-phase in [`docs/roadmap.md`](docs/roadmap.md).

---

## 4. Git Workflow: Branch Naming & Progressive Commits

### Branch Naming

All work happens on short-lived branches created from `main`:

```text
<type>/<short-kebab-description>
```

Examples:
- `feat/phase-3-approval-gate`
- `fix/symlink-escape`
- `docs/standardize-docs-and-agents`
- `refactor/worker-ipc-framing`

### Progressive Commits

Build and commit **progressively** in small, logical, reviewable steps rather than bundling an entire feature or phase into one monolithic commit:

- Split work along natural architectural seams (for example: `schema`/contracts -> core package -> CLI/MCP wiring -> adversarial/integration tests -> `docs`).
- Every commit should leave the repository in a coherent, buildable state so `git bisect` and code review remain clean.
- Keep commits focused: avoid mixing unrelated refactors, formatting churn, and feature work in the same commit.

### Commit Messages & PR Titles

Commit messages and PR titles MUST follow **Conventional Commits 1.0.0** (enforced by `lefthook` and CI):

```text
<type>(<scope>): <imperative lowercase summary>
```

- Subject line: **72 characters or fewer**, starts with lowercase `[a-z0-9]`, imperative mood, **no trailing period**.
- Allowed types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`, `build`, `perf`.
- Allowed scopes: `runtime`, `host`, `registry`, `approval`, `mcp`, `cli`, `schema`, `docs`, `ci`, `deps`.
- Breaking changes use `<type>(<scope>)!:` or a `BREAKING CHANGE:` footer.
- **Never bypass Git hooks (`--no-verify`).**

---

## 5. Mandatory Toolchain Rules

- Use **`mise`** for all tasks. Never introduce `Makefile`, `package.json`, `node_modules`, or Python scripts.
- Before finishing any task, run the full verification gate:
  ```sh
  mise run check
  ```
- Individual tasks:
  - `mise run fmt` — format Go source files (`gofmt -w .`)
  - `mise run fmt-check` — verify Go formatting
  - `mise run lint` — run `golangci-lint run ./...`
  - `mise run test` — run `go test -race ./...`
  - `mise run validate-schemas` — run pure-Go schema and example bundle contract tests
  - `mise run build` — compile the static binary to `bin/knurl`

---

## 6. Docs, Schema & Example Synchronization

- Always keep [`docs/specs/`](docs/specs/), [`schemas/`](schemas/), [`internal/contract/`](internal/contract/), and [`examples/`](examples/) strictly synchronized.
- Keep `README.md` focused on the core idea, installation, and usage; keep phased planning in [`docs/roadmap.md`](docs/roadmap.md) and technical specifications in [`docs/`](docs/README.md).
- When authoring or modifying tool bundles, consult the JS host spec at [`docs/specs/js-host-api.md`](docs/specs/js-host-api.md).
- Before adding or modifying any host capability, consult [`docs/specs/security-model.md`](docs/specs/security-model.md).
- Record irreversible or cross-cutting architectural decisions as numbered ADRs under [`docs/adr/`](docs/adr/).

---

## 7. Non-Negotiable Security & Architecture Invariants

1. **No embedded LLM, no background daemon, no cloud sync.** `knurl` is a stateless CLI + stdio MCP server.
2. **Synchronous `goja` JS only.** No `async`/`await`, no `Promise`, no timers, no Node APIs, no multi-file JS imports.
3. **Subprocess isolation.** Tool JS executes exclusively inside `knurl _worker` with hard timeouts, memory/stack/rlimit safeguards, process-group cleanup, and bounded IPC/output sizes.
4. **Non-bypassable human approval.** Agents can only submit proposals (`knurl_propose`) to `~/.config/knurl/staging/<proposal-id>/`. Agents must never write directly to active `tools/` directories or auto-approve proposals.
5. **No bare binary or shell execution.** Never allow shell interpreters (`sh`, `bash`, `zsh`, `powershell`, `cmd`, etc.) or unconstrained command strings in `knurl.exec`; every command must be an approved argument template with literal `args[0]` and `\A(?:...)\z`-anchored RE2-validated `${arg:...}` placeholders.
6. **Symlink-safe filesystem confinement.** All `knurl.fs` access must go through `os.Root` confinement—never raw `os.Open` on user-supplied paths.
7. **Host-only secret injection & redaction.** Secrets are resolved at the Go boundary, never exposed to JS source or LLM context, and scrubbed from stdout, stderr, diagnostics, and errors.
8. **Static MCP surface.** Expose exactly the 5 static MCP meta-tools (`knurl_discover`, `knurl_inspect`, `knurl_run`, `knurl_propose`, `knurl_status`) via `github.com/modelcontextprotocol/go-sdk/mcp`.
