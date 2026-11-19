// Package host enforces knurl's capability boundary around goja JavaScript
// execution: os.Root-confined filesystem reads/globs/stats, read-only git
// inspection, domain-allowlisted HTTP requests with SSRF protections,
// constrained argv-only exec templates, and host-side secret injection and
// redaction.
//
// Host primitive implementations begin in Phase 1 and Phase 2.
package host
