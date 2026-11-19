// Package registry resolves and reads tool bundles. In Phase 1 it provides
// a minimal local bundle loader by directory path or within --tools-dir; full
// two-tier repo/global resolution and atomic active.json pointers arrive in
// Phase 2.
package registry
