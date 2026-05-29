package dbadmin

import (
	"encoding/json"
	"net/http"
)

// writeError emits the canonical Error envelope (see errors.go) at the
// given HTTP status. Used by the engine's handler middleware to keep
// every error response shape identical across endpoints.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": Error{Code: code, Message: message},
	})
}
