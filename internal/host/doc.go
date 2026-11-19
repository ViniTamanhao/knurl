// Package host enforces knurl's capability boundary around goja JavaScript
// execution: os.Root-confined filesystem reads/globs/stats, read-only git
// inspection, constrained argv-only exec templates, sanitized child
// environments, and host-side secret injection and redaction.
package host
