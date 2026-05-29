package history

// Test-only knobs. This file is compiled only during `go test`.

// ForceLikePath flips a Store into the LIKE-search code path, even when
// the underlying SQLite build supports FTS5. Used to exercise the
// fallback search in unit tests without spinning up an FTS5-less
// SQLite build.
func ForceLikePath(s Store) {
	if ss, ok := s.(*sqliteStore); ok {
		ss.hasFTS = false
	}
}

// HasFTS reports whether the store is using the FTS5 search path.
// Test-only window into private state.
func HasFTS(s Store) bool {
	if ss, ok := s.(*sqliteStore); ok {
		return ss.hasFTS
	}
	return false
}
