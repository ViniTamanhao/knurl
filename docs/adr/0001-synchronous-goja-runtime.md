# ADR 0001: Synchronous `goja` JavaScript Runtime

- **Status:** Accepted
- **Date:** 2026-11-19
- **Decision Makers:** `knurl` core maintainers

## Context

`knurl` needs an embeddable scripting runtime inside a single static Go binary so AI agents can author and repair portable tools without requiring external runtimes (Node.js, Python, Deno) or spawning raw shell scripts.

The choice of embedded language directly affects:
1. First-try code generation quality from diverse LLMs (Claude, Codex, Gemini, open models).
2. Binary size, CGO-free cross-compilation, and sandboxability.
3. Complexity of host bindings and timeout/cancellation guarantees.

## Decision

Embed **`github.com/dop251/goja`** running **strictly synchronous ES6 JavaScript**, invoked via `function main(input)` inside an isolated `knurl _worker` subprocess.

Explicitly omit `async`/`await`, `Promise` microtask scheduling across host I/O, timers (`setTimeout`/`setInterval`), `require`/`import`, Node APIs, and Web APIs (`fetch`, `Buffer`, `console`).

## Alternatives Considered

1. **Starlark (`go.starlark.net`)**: Clean deterministic execution, but LLMs frequently confuse Starlark restrictions with full Python (importing `json`, `os`, `re`, `subprocess`, comprehensions/exceptions differences), causing avoidable syntax and runtime failures.
2. **Lua (`gopher-lua`)**: Fast and lightweight, but 1-based indexing, `nil` table semantics, and lack of native JSON/regex ergonomics increase LLM off-by-one and serialization mistakes.
3. **WebAssembly (`wazero`)**: Strong isolation, but requires an external compiler toolchain (TinyGo/Rust/Clang) to author or repair tools—violating `knurl`'s zero-dependency, human-inspectable source workflow.
4. **Async JS with `goja_nodejs` / event loop**: Adds Node-compatible surface expectations, callback/timer scheduling bugs, and harder timeout enforcement without helping sequential CLI/MCP tools.

## Consequences

- **Positive:**
  - 100% pure Go (zero CGO), keeping single-binary cross-compilation trivial.
  - LLMs already write fluent ES6 JavaScript (`JSON.parse`, `RegExp`, `Map`/`Set`, `try/catch`, `Array.prototype`) with high first-try accuracy.
  - Synchronous host calls fit naturally with Go `context.Context` deadlines and `_worker` subprocess cancellation (`runtime.Interrupt` + process-group `SIGKILL`).
  - Tiny, readable host specification (~1 printed page in [`../specs/js-host-api.md`](../specs/js-host-api.md)).
- **Negative:**
  - Concurrent I/O fan-out (e.g., parallel HTTP calls from JS) is not exposed in v0; acceptable because `knurl` tools orchestrate deterministic local checks and CLI workflows rather than high-concurrency scraping.
  - `goja` does not provide OS-level memory/CPU isolation by itself, which is why every execution runs inside a short-lived `knurl _worker` subprocess with hard wall-clock timeouts and byte limits.
