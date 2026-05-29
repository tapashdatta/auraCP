package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// readJSON decodes the request body into dst. Enforces:
//   - http.MaxBytesReader at maxBytes
//   - DisallowUnknownFields (rejects extra JSON fields)
//   - exactly one JSON value (no trailing garbage)
//
// Returns an error suitable for mapErr.
func readJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20 // 1 MiB default
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("readJSON: %w", err)
	}
	// Reject trailing JSON tokens.
	if dec.More() {
		return errors.New("readJSON: unexpected trailing data after JSON value")
	}
	return nil
}

// writeJSON serializes v at the given status. Sets Content-Type and
// X-Content-Type-Options.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
