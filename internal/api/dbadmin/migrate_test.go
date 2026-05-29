package dbadmin

import (
	"path/filepath"
	"testing"

	"github.com/auracp/auracp/internal/store"
)

// TestRunMigrations_Idempotent ensures RunMigrations is safe to call on a
// freshly-open store AND a re-open of the same database.
func TestRunMigrations_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auracp.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// store.Open already calls extra migrators via the init() hook in
	// this package, so the tables should exist now.
	rows, err := st.DB.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'aura_db_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		found = append(found, n)
	}
	if len(found) < 2 {
		t.Fatalf("expected aura_db_connections + aura_db_grants tables; got %v", found)
	}

	// Idempotent second call.
	if err := RunMigrations(st.DB); err != nil {
		t.Fatalf("second RunMigrations: %v", err)
	}
	st.Close()
}
