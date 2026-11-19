# Contributing to `knurl`

Thank you for contributing to `knurl`. Every change—whether documentation, JSON Schema, or Go code—must preserve `knurl`'s core invariants: local-first execution, explicit capability boundaries, host-injected secrets, and non-bypassable human approval.

## 1. Local Toolchain Setup

`knurl` uses [`mise`](https://mise.jdx.dev/) as the single source of truth for tool versions (`go`, `golangci-lint`, `lefthook`) and verification tasks. No Makefiles, Node.js, or Python dependencies are used.

```sh
# 1. Install pinned Go, golangci-lint, and lefthook versions
mise install

# 2. Install local Git hooks (commit-msg and pre-commit)
mise run hooks

# 3. Run the full verification gate
mise run check
```

Available `mise` tasks in [`mise.toml`](mise.toml):

| Task | Purpose |
| :--- | :--- |
| `mise run fmt` | Format Go files with `gofmt -w .` |
| `mise run fmt-check` | Fail if any Go file is unformatted |
| `mise run lint` | Run `golangci-lint run ./...` using `.golangci.yml` |
| `mise run test` | Run `go test -race ./...` |
| `mise run validate-schemas` | Run pure-Go JSON Schema and example bundle contract tests |
| `mise run build` | Build the binary to `bin/knurl` |
| `mise run check` | Composite gate running `fmt-check`, `lint`, `test`, `validate-schemas`, and `build` |
| `mise run hooks` | Install local `lefthook` Git hooks |

## 2. Branching Model

All work happens on short-lived branches off `main`:

```text
<type>/<short-kebab-description>
```

Examples:
- `chore/phase-0-foundation`
- `feat/worker-ipc`
- `fix/symlink-escape`
- `docs/mcp-error-codes`

## 3. Commit Message Standard

This repository enforces [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/) via the `commit-msg` hook in [`lefthook.yml`](lefthook.yml):

```text
<type>(<scope>): <imperative lowercase summary>
```

Rules:
- Subject line must be **72 characters or fewer**.
- Summary must start with a lowercase alphanumeric character (`[a-z0-9]`), use the imperative mood, and **must not** end with a trailing period.
- Breaking changes use `<type>(<scope>)!:` or a `BREAKING CHANGE:` footer.

### Allowed Types

| Type | Usage |
| :--- | :--- |
| `feat` | New user-facing or contract capability |
| `fix` | Bug fix or security boundary fix |
| `docs` | Documentation, specs, or ADRs |
| `chore` | Tooling, repo maintenance, hook/config updates |
| `refactor` | Code restructuring without behavior change |
| `test` | Adding or updating tests |
| `ci` | CI workflows or automation |
| `build` | Build system or release packaging |
| `perf` | Performance improvements |

### Allowed Scopes

| Scope | Area |
| :--- | :--- |
| `runtime` | `goja` execution engine and `_worker` subprocess IPC |
| `host` | Host primitives (`fs`, `git`, `http`, `exec`), secret injection, redaction |
| `registry` | Two-tier repo/global tool lookup and versioning |
| `approval` | Staging, validation, dry-run, capability diffing, approve/rollback |
| `mcp` | Static MCP meta-tools over stdio |
| `cli` | `knurl` CLI flags, subcommands, and human formatting |
| `schema` | `schemas/*.schema.json` and `internal/contract` |
| `docs` | `README.md`, `docs/`, `CONTRIBUTING.md`, `AGENTS.md` |
| `ci` | `mise.toml`, `lefthook.yml`, `.github/` |
| `deps` | Go module dependency updates |

## 4. Pull Requests

- **PR Titles** must match the exact Conventional Commit format above (enforced by CI in [`.github/workflows/ci.yml`](.github/workflows/ci.yml)).
- Fill out all sections of [`.github/pull_request_template.md`](.github/pull_request_template.md).
- Every PR must pass `mise run check` with zero warnings or errors.

## 5. Docs-First Policy & ADRs

- **Contracts before code:** Changes to tool manifests, test schemas, JS host APIs, or MCP payloads must update [`docs/specs/`](docs/specs/), [`schemas/`](schemas/), and [`internal/contract/`](internal/contract/) together in sync.
- **Architecture Decision Records:** Any irreversible or cross-cutting architectural decision must be recorded as a numbered ADR under [`docs/adr/`](docs/adr/) (`NNNN-kebab-title.md`).
