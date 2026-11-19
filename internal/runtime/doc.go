// Package runtime implements isolated synchronous goja execution inside the
// ephemeral `knurl _worker` subprocess, including single-shot NDJSON IPC over
// stdin/stdout pipes, wall-clock timeout enforcement, and process-group
// cleanup.
package runtime
