# knurl

> **Tools your agents forge. Typed to run, safe to keep, zero shell required.**

`knurl` is a single-binary, local-first Go tool vault that turns ephemeral scripts written by AI coding agents into durable, typed, capability-scoped tools that both humans and agents can reuse permanently—from the terminal or over stdio MCP.

The name **knurl** comes from machining: cutting a textured grip into smooth metal so a human hand can reliably hold and turn a tool. With `knurl`, agents write the code while humans hold the grip.

---

## Core Idea

AI coding agents routinely solve real problems with ad-hoc shell or Python scripts, only for those solutions to vanish at the end of a session—or linger as brittle, over-privileged scripts requiring unrestricted Bash access.

`knurl` replaces loose scripts with **versioned tool bundles** (`manifest.json` + synchronous ES6 `tool.js` + mock-backed `tests.json`) executed inside an isolated subprocess with host-enforced capabilities and an explicit human approval gate:

| Challenge with Loose Scripts | How `knurl` Solves It |
| :--- | :--- |
| **Permission fatigue or unrestricted shell access** | **Approve-once capability boundaries:** Review exact filesystem, Git, HTTP, and templated `exec` permissions once; run with zero Bash access. |
| **Exposed credentials** | **Host-injected secrets:** Secrets are declared by name in `manifest.json`, resolved only at the Go host boundary, and redacted from all outputs and errors. |
| **Noisy, unstructured output** | **Typed JSON Schema contracts:** Inputs and outputs are validated against JSON Schema Draft 2020-12 before and after execution. |
| **Runtime & environment drift** | **Zero external dependencies:** A single static Go binary embeds a synchronous ES6 JavaScript engine (`goja`) that runs identically across machines. |
| **Locked to shell-capable agents** | **Dual-use portability:** Every approved tool runs natively in human terminals (`knurl run`) and in any MCP-capable client (`knurl mcp`), including clients without shell access. |

### The 4-Step Lifecycle

1. **Solve:** Work with an agent on a real task (e.g., summarizing repository context, checking PR readiness, or running ops diagnostics).
2. **Propose:** Ask the agent to save the workflow. It submits `manifest.json`, synchronous `tool.js`, and `tests.json` to staging via `knurl_propose`.
3. **Validate & Approve:** `knurl` validates schemas, compiles the script, runs mock tests, previews a safe read-only dry-run when eligible, computes capability diffs, and waits for explicit human approval (`knurl approve`).
4. **Reuse, Discover & Repair:** Run approved tools from the CLI or let future agents discover and invoke them via MCP. When a tool needs an update, an agent proposes `v2` through the same approval gate, with one-command `knurl rollback`.

### Key Properties

- **No embedded LLM, no background daemon, no cloud account.** `knurl` is a stateless CLI and stdio MCP server.
- **Subprocess isolation.** Every execution runs in a short-lived `knurl _worker` subprocess with hard timeouts, memory/stack/rlimit caps, process-group cleanup, and bounded IPC frames.
- **Two-tier versioned registry.** Repo-local tools (`./.knurl/tools/`) automatically override global user tools (`~/.config/knurl/tools/`).
- **Static MCP surface.** Exposes a fixed set of meta-tools over stdio so tool catalogs never bloat agent context windows.

---

## Installation

### From Source with `go install`

Requires **Go 1.27+**:

```sh
go install github.com/ViniTamanhao/knurl/cmd/knurl@latest
```

### From Source with `mise` (Recommended for Development)

[`mise`](https://mise.jdx.dev/) manages the pinned Go, linter, and Git hook versions defined in [`mise.toml`](mise.toml):

```sh
git clone https://github.com/ViniTamanhao/knurl.git
cd knurl
mise install
mise run build
```

This builds the static binary at `./bin/knurl`. Verify the installation:

```sh
./bin/knurl version
```

---

## Usage

### Run a Tool from the CLI (`knurl run`)

By default, `knurl run <tool>` resolves versioned bundles (`tools/<name>/v<N>/`) across the two-tier registry—first walking upward from `--workdir` for `./.knurl/tools` (stopping at `.git`), then falling back to the global tools directory (`$KNURL_HOME/tools`, `$KNURL_CONFIG_DIR/tools`, `$XDG_CONFIG_HOME/knurl/tools`, or `~/.config/knurl/tools`):

```sh
# Resolve automatically (repo-local overrides global)
knurl run pr-ready

# Pin scope, version, and full JSON execution envelope
knurl run pr-ready --scope repo --version v2 --json

# Pass typed arguments or raw JSON input
knurl run vps-health --scope global --arg host=prod-01.example.com --arg user=ops
knurl run repo-context --input '{"include_tree":false}'
```

### Try the Bundled Examples

Use `--tools-dir` (or `$KNURL_TOOLS_DIR`) or pass a direct bundle directory path to run the reference bundles in [`examples/`](examples/):

```sh
# Read-only repository summary (knurl.fs + knurl.git)
./bin/knurl run repo-context --tools-dir ./examples
./bin/knurl run repo-context --tools-dir ./examples --arg recent_commits=5 --json
./bin/knurl run ./examples/repo-context --input '{"include_tree":false}'

# Permission-scoped ops diagnostic (templated exec + allowlisted HTTPS + host secrets)
VPS_SSH_KEY_PATH=~/.ssh/id_ed25519 VPS_HEALTH_TOKEN=tok_example \
  ./bin/knurl run vps-health --tools-dir ./examples \
    --arg host=prod-01.example.com \
    --arg user=ops \
    --arg services=nginx,caddy \
    --arg check_http_url=https://status.example.com/healthz
```

### Connect an MCP Client (`knurl mcp`)

Start `knurl mcp` from any MCP-capable client (such as Claude Desktop, Cursor, or Codex) over stdio. Protocol traffic uses `stdout`, while diagnostics are written strictly to `stderr`:

```json
{
  "mcpServers": {
    "knurl": {
      "command": "/abs/path/to/knurl/bin/knurl",
      "args": [
        "mcp",
        "--workdir", "/abs/path/to/your-workspace"
      ]
    }
  }
}
```

To test directly against the repository's example bundles, add `"--tools-dir", "/abs/path/to/knurl/examples"` to `args`.

### Anatomy of a Tool Bundle

Each versioned tool is a small, human-auditable directory on disk:

```text
tools/<name>/v1/
├── manifest.json   # Metadata, JSON Schema input/output contracts, capabilities, secret names
├── tool.js         # Synchronous ES6 JavaScript entrypoint: function main(input)
├── tests.json      # Mock-backed contract tests
└── approval.json   # Host-written approval audit record (SHA-256 + capability diff)
```

See [`docs/specs/tool-bundle.md`](docs/specs/tool-bundle.md) and [`docs/specs/js-host-api.md`](docs/specs/js-host-api.md) for the complete authoring reference.

---

## Documentation

| Document | Description |
| :--- | :--- |
| [`docs/README.md`](docs/README.md) | Complete technical documentation index and package map |
| [`docs/architecture.md`](docs/architecture.md) | System architecture, `_worker` subprocess isolation, IPC, and registry design |
| [`docs/cli-reference.md`](docs/cli-reference.md) | Technical CLI reference (commands, flags, environment variables, exit codes) |
| [`docs/specs/tool-bundle.md`](docs/specs/tool-bundle.md) | On-disk bundle layout, versioning, precedence, and staging format |
| [`docs/specs/js-host-api.md`](docs/specs/js-host-api.md) | Synchronous JavaScript host API (`fs`, `git`, `http`, `exec`, `secrets`, `log`) |
| [`docs/specs/mcp-protocol.md`](docs/specs/mcp-protocol.md) | Static MCP meta-tool contracts, payload schemas, and canonical error codes |
| [`docs/specs/security-model.md`](docs/specs/security-model.md) | Threat model, capability boundaries, SSRF/exec/fs hardening, and redaction |
| [`docs/adr/`](docs/adr/) | Architecture Decision Records |
| [`docs/roadmap.md`](docs/roadmap.md) | Phased v0 implementation plan, flagship workflows, and explicit non-goals |

---

## Development & Contributing

```sh
mise install        # Install pinned Go, golangci-lint, and lefthook
mise run hooks      # Install commit-msg and pre-commit Git hooks
mise run check      # Run fmt-check, lint, test, validate-schemas, and build
```

See [`CONTRIBUTING.md`](CONTRIBUTING.md) for commit, branching, and PR standards, and [`AGENTS.md`](AGENTS.md) for the AI coding agent operational guide.
