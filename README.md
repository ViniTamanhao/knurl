# knurl

> **Tools your agents forge. Typed to run, safe to keep, zero shell required.**

The name **knurl** comes from machining: cutting a textured grip into smooth metal so a human hand can reliably hold and turn a tool. `knurl` takes ephemeral, one-off solutions that AI coding agents write during a session and turns them into durable, capability-scoped tools that both humans and agents can reuse permanently.

`knurl` is a single-binary, local-first, open-source Go tool vault. Agents author synchronous JavaScript tools (`tool.js`) paired with typed JSON Schemas (`manifest.json`) and mock-backed tests (`tests.json`), submit them to a staging gate, and—after explicit human inspection and approval—run them across sessions and clients via CLI or stdio MCP.

> **Status:** **Phase 1 (Core Execution Slice / Kill-or-Commit Gate)** is implemented—`knurl _worker` subprocess isolation (`goja`), `os.Root` filesystem confinement, read-only `knurl.git`, templated `knurl.exec`, host secret binding & redaction, `knurl run`, and `knurl mcp` (`knurl_run`). Two-tier registry, `knurl.http`, and the `knurl_propose`/`knurl approve` workflow arrive in Phases 2–3.

## Core Principles

1. **No embedded LLM, no cloud account, no background daemon.** `knurl` is strictly an execution, persistence, and governance layer for the agent or terminal workflow you already use.
2. **Dual-use by design.** Every approved tool runs directly from the terminal (`knurl run pr-ready --base main`) and from any MCP-capable agent over stdio.
3. **Human-owned, non-bypassable control.** Agents cannot write directly to active tool directories or self-approve capabilities. Every creation or repair passes through staging, automated validation, dry-run preview, and explicit human approval.
4. **Agents write the code; humans hold the grip.** Agents author and repair `tool.js`; humans inspect capability diffs, approve once, and keep clean versioned files on disk.

## Why `knurl` vs. `SKILL.md` + Loose Scripts

| Problem with `SKILL.md` + Scripts | How `knurl` Solves It |
| :--- | :--- |
| **Permission fatigue or YOLO mode:** Shell scripts force endless bash prompts or unrestricted terminal access. | **Approve-once capability boundary:** Approve `vps-health v1` once with exact constrained templates and read-only scope; agents run it with zero Bash access. |
| **Exposed credentials:** Scripts touching SSH keys, `.env` files, or API tokens put secrets within reach of confused or prompt-injected agents. | **Host-injected secrets:** Secrets are declared by name in `manifest.json`, resolved only at the Go host boundary, and redacted from stdout, stderr, diagnostics, and errors. |
| **Unstructured output:** Ad-hoc scripts dump noisy ANSI logs that waste context tokens and break parsing. | **Enforced typed I/O contracts:** Inputs and outputs are validated against JSON Schema Draft 2020-12 before and after execution (`--json` for agents/pipes, readable output for humans). |
| **Environment rot:** Scripts break across Python venvs, Node versions, and macOS/Linux flag differences. | **Zero-dependency portability:** One static Go binary executes synchronous ES6 JavaScript via an embedded runtime identically everywhere. |
| **Locked to shell agents:** Terminal scripts cannot run in clients without Bash access. | **Cross-agent & non-shell portability:** Tools forged in Codex or Claude Code immediately work in Claude Desktop, IDE agents without shells, CI, and human terminals. |

## The 4-Step Lifecycle

1. **Solve:** Work with an agent on a real task (e.g., summarizing a repo or checking PR readiness).
2. **Propose:** Ask the agent to save the workflow. It submits `manifest.json`, synchronous `tool.js`, and `tests.json` via `knurl_propose`.
3. **Validate & Approve:** `knurl` stages the bundle under `~/.config/knurl/staging/<proposal-id>/`, validates schemas, compiles the script, executes mock tests, runs a read-only working-directory dry-run when safe, and waits for `knurl approve --last`.
4. **Reuse, Discover & Repair:** Run `knurl run <tool>` locally or let future agents call `knurl_discover`, `knurl_inspect`, and `knurl_run`. If a tool breaks later, an agent proposes `v2` through the same approval gate—with one-command `knurl rollback <tool>`.

## Flagship v0 Workflows

- **`repo-context`** ([`examples/repo-context/`](examples/repo-context/)): Read-only repository summary validating cross-agent discovery, structured JSON output, and token savings—with zero shell access.
- **`pr-ready`** *(Phase 2/3)*: Repo-local (`./.knurl/tools`) PR readiness checks validating two-tier precedence and the break → repair → diff-approve → rollback lifecycle.
- **`vps-health`** ([`examples/vps-health/`](examples/vps-health/)): Permission-scoped ops diagnostic validating standalone human CLI utility, constrained SSH argument templates, and host-injected secrets.

## Architecture Snapshot

- **Control Plane + Isolated `_worker`:** Every execution spawns a short-lived `knurl _worker` subprocess communicating via length/line-bounded NDJSON over pipes, enforcing hard timeouts, process-group cleanup, and strict byte limits.
- **Synchronous `goja` Runtime:** Pure-Go ES6 JavaScript (`dop251/goja`) with no `async/await`, timers, Node APIs, or imports—keeping the host surface tiny and LLM code generation deterministic.
- **Two-Tier Registry & Staging:** Repo-local `./.knurl/tools/` overrides global `~/.config/knurl/tools/`; unapproved changes live strictly in `~/.config/knurl/staging/`.
- **5 Static MCP Meta-Tools:** `knurl_discover`, `knurl_inspect`, `knurl_run`, `knurl_propose`, and `knurl_status` over stdio (`github.com/modelcontextprotocol/go-sdk/mcp`).

## Phase 1 Quickstart (Build, CLI & Stdio MCP)

Build the single static `knurl` binary with `mise`:

```sh
mise run build
```

### Run a Tool from the CLI (`knurl run`)

Execute `examples/repo-context` against the current repository:

```sh
./bin/knurl run repo-context --tools-dir ./examples
./bin/knurl run repo-context --tools-dir ./examples --arg recent_commits=5 --json
./bin/knurl run ./examples/repo-context --input '{"include_tree":false}'
```

Execute `examples/vps-health` (SSH diagnostics with host-injected `VPS_SSH_KEY_PATH`):

```sh
VPS_SSH_KEY_PATH=~/.ssh/id_ed25519 ./bin/knurl run vps-health --tools-dir ./examples \
  --arg host=prod-01.example.com --arg user=ops --arg services=nginx,caddy
```

*(Note: `knurl.http` is deferred to Phase 2, so omit `--arg check_http_url=...` in Phase 1.)*

### Connect an MCP Client (`knurl mcp`)

In Phase 1, `knurl mcp` serves the **`knurl_run`** tool over stdin/stdout (writing diagnostics strictly to stderr). Add it to your MCP client configuration (Claude Desktop, Claude Code, Codex, etc.):

```json
{
  "mcpServers": {
    "knurl": {
      "command": "/abs/path/to/knurl/bin/knurl",
      "args": [
        "mcp",
        "--tools-dir", "/abs/path/to/knurl/examples",
        "--workdir", "/abs/path/to/your-workspace"
      ]
    }
  }
}
```

## Development Setup

`knurl` uses [`mise`](https://mise.jdx.dev/) for pinned tool versions and tasks:

```sh
mise install        # Install pinned go, golangci-lint, and lefthook
mise run hooks      # Install commit-msg and pre-commit git hooks
mise run check      # Run fmt-check, lint, test, validate-schemas, and build
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for commit/PR conventions and [`docs/README.md`](docs/README.md) for full architecture and protocol specifications.
