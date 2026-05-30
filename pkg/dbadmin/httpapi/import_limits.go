package httpapi

import "time"

// Import caps. Hard ceilings the import handler applies regardless of
// operator-supplied request limits. The values are stricter than the
// export-side caps because every imported row is one write round-trip,
// not a streaming read — wall-clock cost scales linearly with row count.
const (
	// importMaxBodyBytes is the multipart body ceiling. The router
	// installs the same value via maxBody() (see router.go) so the
	// gate fires at the HTTP layer; the handler's ParseMultipartForm
	// budget is re-checked against this same constant for defence in
	// depth.
	importMaxBodyBytes int64 = 64 << 20

	// importInMemoryThreshold is the multipart-form in-memory budget.
	// Bytes beyond this spool to $TMPDIR (mime/multipart.FileHeader
	// transparently swaps to disk via os.CreateTemp). 8 MiB keeps
	// small CSV / NDJSON in RAM while letting bigger payloads stream
	// through disk-backed temp files.
	importInMemoryThreshold int64 = 8 << 20

	// importMaxRowsHardCap is the absolute ceiling on rows the import
	// handler will Insert / UpdateByPK before stopping with truncated=true.
	// Stricter than the export cap (1M) because every imported row is
	// one writer-side round-trip, not a single streaming SELECT.
	importMaxRowsHardCap int64 = 100_000

	// importTimeoutHard is the absolute deadline for one import request.
	// Independent of the route's perRouteTimeout (router.go sets 300s
	// for /import; this constant matches so the two stay synchronised).
	importTimeoutHard = 5 * time.Minute
)

// importLocks is the per-server in-flight import tracker. Mirrors
// exportLocks. Reuses the same exportLockManager type — the gate is
// agnostic to the action it guards; the lock manager only cares about
// the userID. A separate instance keeps export + import concurrency
// independent (a user mid-export can still kick off an import).
var importLocks = newExportLockManager()
