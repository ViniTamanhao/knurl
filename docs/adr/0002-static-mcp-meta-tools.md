# ADR 0002: Five Static MCP Meta-Tools

- **Status:** Accepted
- **Date:** 2026-11-19
- **Decision Makers:** `knurl` core maintainers

## Context

`knurl` exposes saved tools to AI agents via MCP over stdio (`github.com/modelcontextprotocol/go-sdk/mcp`). There are two ways to surface a growing library of user/agent-forged tools over MCP:

1. **Dynamic MCP tool registration:** Register every saved `knurl` tool as a separate MCP tool in `tools/list`, sending `notifications/tools/list_changed` whenever tools are added or updated.
2. **Static MCP meta-tools:** Expose a small fixed set of meta-tools that let agents discover, inspect, run, propose, and check proposals against the registry.

## Decision

Expose **exactly 5 static MCP meta-tools** over stdio (`knurl_discover`, `knurl_inspect`, `knurl_run`, `knurl_propose`, `knurl_status`), as specified in [`../specs/mcp-protocol.md`](../specs/mcp-protocol.md).

## Alternatives Considered

1. **Dynamic per-tool MCP registration (`tools/list` per saved tool):**
   - Bloats the LLM system prompt and context window with every tool's full JSON Schema even when unrelated to the current task.
   - Suffers from uneven client support for dynamic `notifications/tools/list_changed` refreshes (many desktop/IDE MCP clients require manual reconnect or session restart).
   - Cannot represent the proposal/staging workflow (`knurl_propose`, `knurl_status`) cleanly as a first-class capability.

## Consequences

- **Positive:**
  - Constant, minimal MCP tool-schema footprint in agent context regardless of whether the user has 3 or 300 saved tools.
  - Works reliably across every MCP stdio client (Claude Code, Codex, Claude Desktop, Gemini CLI, restricted IDE agents) with zero dynamic refresh dependencies.
  - Progressive disclosure by construction: `knurl_discover` returns names/descriptions/flags only; `knurl_inspect` fetches schemas on demand; `tool.js` source stays out of agent context during normal reuse.
- **Negative:**
  - Calling a saved tool requires passing `{ name, input }` to `knurl_run` rather than calling a top-level MCP tool name directly, and agents need to inspect an unfamiliar tool (`knurl_inspect`) before invoking it. Mitigated by concise `knurl_discover` metadata and a ~30-line companion `SKILL.md` in Phase 4.
