// Package runtime implements isolated synchronous goja execution inside the
// ephemeral `knurl _worker` subprocess, including framed stdin/stdout JSON IPC,
// wall-clock timeout enforcement, and process-group cleanup.
//
// Runtime execution is implemented starting in Phase 1; Phase 0 freezes only
// the IPC contracts in package contract.
package runtime
