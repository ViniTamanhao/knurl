# ADR 0003: Two-Tier Registry, Explicit Version Pointers, and Staging Gate

- **Status:** Accepted
- **Date:** 2026-11-19
- **Decision Makers:** `knurl` core maintainers

## Context

`knurl` serves both project-specific workflows (like `pr-ready` tied to a repository's build/test conventions) and personal cross-project utilities (like `vps-health`). At the same time, untrusted or mistaken agents propose new tools and repairs, so active executable bundles must never be modified without human consent.

## Decision

1. **Two-tier registry:**
   - **Repo-local:** `./.knurl/tools/<name>/v<N>/` (resolved walking upward from `cwd` up to `.git` root or filesystem root), intended to be committed and shared with teammates.
   - **Global:** `$KNURL_HOME/tools/<name>/v<N>/` or `$XDG_CONFIG_HOME/knurl/tools/<name>/v<N>/` (default `~/.config/knurl/tools/<name>/v<N>/`).
   - In `auto` scope, repo-local shadows global.
2. **Explicit `active.json` version pointer instead of symlinks:**
   - Each tool directory stores immutable `v<N>/` directories plus an atomically written `active.json` (`active_version`, `previous_version`, `updated_at`), enabling portable one-command rollback across Unix and Windows without symlink edge cases.
3. **Mandatory user-local staging gate:**
   - All agent-submitted proposals (`knurl_propose`)—even those targeting `scope: "repo"`—are written exclusively to global staging (`~/.config/knurl/staging/<proposal-id>/`).
   - Only an explicit human CLI action (`knurl approve`) verifies the staged SHA-256 digest and atomically installs `v<N>/` into the target registry tier.

## Alternatives Considered

1. **Single global registry:** Cannot share repo-specific workflows via Git or override a personal tool per project.
2. **Writing proposals into `./.knurl/staging/` inside the workspace:** Pollutes `git status` with unapproved drafts and creates risk if an agent can directly edit workspace files before approval.
3. **Symlink `current -> v<N>`:** Fragile across Windows filesystems, Git checkouts, and archive tools.

## Consequences

- **Positive:**
  - Repo-local tools live cleanly alongside code in Git while unapproved agent drafts remain isolated in user-local staging.
  - Immutable `v<N>/` directories plus `active.json` make `knurl rollback` atomic, instantaneous, and auditable via `approval.json`.
  - Clean separation between untrusted write paths (`staging/`) and trusted execution paths (`tools/`).
- **Negative:**
  - Requires explicit `knurl approve` in a human terminal before an agent's newly proposed tool or repair takes effect; this friction is intentional and central to `knurl`'s security model.
