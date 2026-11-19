// Package registry resolves and reads approved tool bundles across knurl's
// two-tier local store (repo-local ./.knurl/tools overriding global
// ~/.config/knurl/tools) using atomic active.json version pointers.
//
// Registry filesystem operations are implemented starting in Phase 2.
package registry
