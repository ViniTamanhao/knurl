// Package approval manages the non-bypassable human approval gate: staging
// submitted proposals in ~/.config/knurl/staging/<proposal-id>/, running
// schema/compile/test/dry-run validation, computing capability-escalation diffs,
// promoting SHA-256-verified bundles, and performing atomic version rollbacks.
//
// Approval workflow implementation begins in Phase 3.
package approval
