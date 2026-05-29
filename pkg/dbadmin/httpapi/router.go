package httpapi

import (
	"net/http"
	"time"
)

// routes builds the chi-style ServeMux for the engine. Uses Go 1.22+
// method+pattern routing on net/http.ServeMux.
//
// Middleware ordering:
//
//	shutdownGate → requestID → recoverer → maxBody → perRouteTimeout →
//	authn → csrf → rateLimit → audit → handler
//
// Reads skip CSRF and use the reading rate-limit class; mutations
// include CSRF + the mutating class. The WS route excludes CSRF +
// per-route timeout (it manages its own).
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	defaultTimeout := 30 * time.Second
	queryTimeout := 60 * time.Second
	testTimeout := 5 * time.Second

	// Common middleware chains.
	read := func(d time.Duration, h http.HandlerFunc) http.Handler {
		return chain(h,
			shutdownGate(s),
			requestID(),
			recoverer(s),
			maxBody(1<<20),
			perRouteTimeout(d),
			authn(s),
			rateLimit(s, rateClassReading),
			audit(s),
		)
	}
	write := func(d time.Duration, h http.HandlerFunc) http.Handler {
		return chain(h,
			shutdownGate(s),
			requestID(),
			recoverer(s),
			maxBody(1<<20),
			perRouteTimeout(d),
			authn(s),
			csrf(s),
			rateLimit(s, rateClassMutating),
			audit(s),
		)
	}

	// Connections.
	mux.Handle("GET /connections", read(defaultTimeout, handleListConnections(s)))
	mux.Handle("POST /connections", write(defaultTimeout, handleCreateConnection(s)))
	mux.Handle("GET /connections/{id}", read(defaultTimeout, handleGetConnection(s)))
	mux.Handle("PUT /connections/{id}", write(defaultTimeout, handleUpdateConnection(s)))
	mux.Handle("DELETE /connections/{id}", write(defaultTimeout, handleDeleteConnection(s)))
	mux.Handle("POST /connections/{id}/test", write(testTimeout, handleTestConnection(s)))
	mux.Handle("POST /connections/{id}/password/reveal", write(defaultTimeout, handleRevealPassword(s)))

	// Schema metadata.
	mux.Handle("GET /connections/{id}/schemas", read(defaultTimeout, handleListSchemas(s)))
	mux.Handle("GET /connections/{id}/schemas/{s}/objects", read(defaultTimeout, handleListObjects(s)))
	mux.Handle("GET /connections/{id}/schemas/{s}/tables/{t}", read(defaultTimeout, handleGetTable(s)))

	// Rows.
	mux.Handle("GET /connections/{id}/schemas/{s}/tables/{t}/rows", read(defaultTimeout, handleReadRows(s)))
	mux.Handle("POST /connections/{id}/schemas/{s}/tables/{t}/rows", write(defaultTimeout, handleInsertRow(s)))
	mux.Handle("PATCH /connections/{id}/schemas/{s}/tables/{t}/rows/{pk}", write(defaultTimeout, handleUpdateRow(s)))
	mux.Handle("DELETE /connections/{id}/schemas/{s}/tables/{t}/rows/{pk}", write(defaultTimeout, handleDeleteRow(s)))

	// SQL.
	mux.Handle("POST /connections/{id}/query", write(queryTimeout, handleQuery(s)))
	mux.Handle("POST /connections/{id}/explain", write(queryTimeout, handleExplain(s)))

	// Classifier preview (UX only; never a security boundary — the actual
	// security re-classify happens inside handleQuery before dispatch).
	classifyTimeout := 10 * time.Second
	mux.Handle("POST /sql/classify", write(classifyTimeout, handleClassify(s)))
	mux.Handle("POST /connections/{id}/classify", write(classifyTimeout, handleClassifyForConnection(s)))

	// WS stream — own timeout management.
	mux.Handle("GET /sql/stream", chain(handleSQLStream(s),
		shutdownGate(s),
		requestID(),
		recoverer(s),
		authn(s),
	))

	// History.
	mux.Handle("GET /connections/{id}/history", read(defaultTimeout, handleListHistory(s)))
	mux.Handle("GET /connections/{id}/history/search", read(defaultTimeout, handleSearchHistory(s)))
	mux.Handle("PATCH /connections/{id}/history/{eid}", write(defaultTimeout, handlePatchHistory(s)))
	mux.Handle("DELETE /connections/{id}/history/{eid}", write(defaultTimeout, handleDeleteHistory(s)))

	// Saved queries.
	mux.Handle("GET /connections/{id}/saved-queries", read(defaultTimeout, handleListSaved(s)))
	mux.Handle("POST /connections/{id}/saved-queries", write(defaultTimeout, handleCreateSaved(s)))
	mux.Handle("DELETE /connections/{id}/saved-queries/{sid}", write(defaultTimeout, handleDeleteSaved(s)))

	// Export/import.
	mux.Handle("POST /connections/{id}/export", write(queryTimeout, handleExport(s)))
	mux.Handle("POST /connections/{id}/import", chain(handleImport(s),
		shutdownGate(s),
		requestID(),
		recoverer(s),
		maxBody(64<<20), // 64 MiB import ceiling
		perRouteTimeout(300*time.Second),
		authn(s),
		csrf(s),
		rateLimit(s, rateClassMutating),
		audit(s),
	))

	// Audit.
	mux.Handle("GET /connections/{id}/audit", read(defaultTimeout, handleListAudit(s)))

	// Step-up.
	mux.Handle("POST /step-up/initiate", chain(handleStepUpInitiate(s),
		shutdownGate(s),
		requestID(),
		recoverer(s),
		maxBody(1<<18),
		perRouteTimeout(defaultTimeout),
		authn(s),
		csrf(s),
		rateLimit(s, rateClassMutating),
		audit(s),
	))
	mux.Handle("POST /step-up/verify", chain(handleStepUpVerify(s),
		shutdownGate(s),
		requestID(),
		recoverer(s),
		maxBody(1<<18),
		perRouteTimeout(defaultTimeout),
		authn(s),
		csrf(s),
		rateLimit(s, rateClassMutating),
		audit(s),
	))

	// Catch-all: emit canonical 404.
	mux.Handle("/", chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "route not found")
	}),
		shutdownGate(s),
		requestID(),
		recoverer(s),
		authn(s),
	))

	return mux
}
