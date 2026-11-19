# AGENTS.md — Operational Guide for AI Coding Agents

This repository builds **`knurl`**, a single-binary, local-first Go tool vault that lets AI agents propose, discover, inspect, and execute capability-scoped synchronous JavaScript tools under human approval.

## Mandatory Toolchain Rules

- Use **`mise`** for all tasks. Never introduce `Makefile`, `package.json`, `node_modules`, or Python scripts.
- Before finishing any task, run:
  ```sh
  mise run check
  ```
- Format Go files with `mise run fmt`, verify lint rules with `mise run lint`, and run tests with `mise run test`.

## Git, Branch & Commit Conventions

- Branch naming: `<type>/<short-kebab-description>`.
- Commit messages and PR titles MUST follow Conventional Commits 1.0.0 (enforced by `lefthook` and CI):
  - Format: `<type>(<scope>): <imperative lowercase summary>` (<= 72 chars, no trailing period).
  - Allowed types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`, `build`, `perf`.
  - Allowed scopes: `runtime`, `host`, `registry`, `approval`, `mcp`, `cli`, `schema`, `docs`, `ci`, `deps`.
- Never bypass Git hooks (`--no-verify`).

## Docs & Schema Synchronization

- Always keep [`docs/specs/`](docs/specs/), [`schemas/`](schemas/), [`internal/contract/`](internal/contract/), and [`examples/`](examples/) strictly synchronized.
- When authoring or modifying example tool bundles or authoring tools in general, consult the concise JS host spec at [`docs/specs/js-host-api.md`](docs/specs/js-host-api.md).
- Before adding or modifying any host capability, consult [`docs/specs/security-model.md`](docs/specs/security-model.md).

## Non-Negotiable Security & Architecture Invariants

1. **No embedded LLM, no background daemon, no cloud sync.** `knurl` is a stateless CLI + stdio MCP server.
2. **Synchronous `goja` JS only.** No `async`/`await`, no `Promise`, no timers, no Node APIs, no multi-file JS imports.
3. **Subprocess isolation.** Tool JS executes exclusively inside `knurl _worker` with hard timeouts, process-group cleanup, and bounded output sizes.
4. **Non-bypassable human approval.** Agents can only submit proposals (`knurl_propose`) to `~/.config/knurl/staging/<proposal-id>/`. Agents must never write directly to active `tools/` directories or auto-approve proposals.
5. **No bare binary or shell execution.** Never allow shell interpreters (`sh`, `bash`, `zsh`, `powershell`, `cmd`, etc.) or unconstrained command strings in `knurl.exec`; every command must be an approved argument template with literal `args[0]` and RE2-validated `${arg:...}` placeholders.
6. **Symlink-safe filesystem confinement.** All `knurl.fs` access must go through `os.Root` confinement—never raw `os.Open` on user-supplied paths.
7. **Host-only secret injection & redaction.** Secrets are resolved at the Go boundary, never exposed to JS source or LLM context, and scrubbed from stdout, stderr, diagnostics, and errors.
8. **Static MCP surface.** Expose exactly the 5 static MCP meta-tools (`knurl_discover`, `knurl_inspect`, `knurl_run`, `knurl_propose`, `knurl_status`) via `github.com/modelcontextprotocol/go-sdk/mcp`.
