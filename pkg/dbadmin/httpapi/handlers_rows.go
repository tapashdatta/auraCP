package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/rows"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

func handleReadRows(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		schemaName := r.PathValue("s")
		table := r.PathValue("t")
		setAuditAction(r.Context(), dbadmin.ActionRowRead, dbadmin.Target{ConnectionID: connID, Schema: schemaName, Object: table})
		if err := schema.ValidateIdentifier(schemaName); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if err := schema.ValidateIdentifier(table); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionRowRead)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		conn, err := openConn(s, r.Context(), c)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		defer conn.Close()
		rdr, err := schema.For(conn, c.Engine)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		op, err := rows.New(conn, rdr, rows.Options{})
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}

		q := r.URL.Query()
		opts := rows.ReadOpts{
			Schema: schemaName,
			Table:  table,
		}
		if v := q.Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "invalid limit")
				return
			}
			opts.Limit = n
		}
		if v := q.Get("offset"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "invalid offset")
				return
			}
			opts.Offset = n
		}
		for _, col := range q["columns"] {
			if col != "" {
				opts.Columns = append(opts.Columns, col)
			}
		}
		for _, sortSpec := range q["sort"] {
			if sortSpec == "" {
				continue
			}
			desc := false
			col := sortSpec
			if strings.HasPrefix(col, "-") {
				desc = true
				col = col[1:]
			}
			opts.Sort = append(opts.Sort, rows.SortKey{Column: col, Descending: desc})
		}
		for _, f := range q["filter"] {
			p, err := parseFilter(f)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, CodeInvalidPredicate, err.Error())
				return
			}
			opts.Filter = append(opts.Filter, p)
		}

		// WIRE-08: clients request the row count once (typically on
		// the first page load) by setting ?total=1. We piggy-back a
		// Count() call under the same conn so the footer can show
		// "Page X of Y". Subsequent page navigations reuse the count.
		wantTotal := false
		if v := q.Get("total"); v == "1" || v == "true" {
			wantTotal = true
		}

		res, err := op.Read(r.Context(), opts)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}

		// WIRE-05: rows.Read returns columns from the driver, which does
		// NOT populate the PrimaryKey flag (the driver only knows column
		// names + DatabaseTypeName + Nullable). We fetch the table once
		// from the schema reader to mark PK columns so the client can
		// distinguish read-only-by-PK from read-only-by-table.
		colDTOs := columnInfosToDTO(res.Columns)
		if tbl, terr := rdr.GetTable(r.Context(), schemaName, table); terr == nil && tbl != nil {
			pkSet := make(map[string]bool, len(tbl.PrimaryKey))
			for _, c := range tbl.PrimaryKey {
				pkSet[c] = true
			}
			for i := range colDTOs {
				if pkSet[colDTOs[i].Name] {
					colDTOs[i].PrimaryKey = true
				}
			}
		}

		resp := readRowsResponse{
			Columns: colDTOs,
			Rows:    res.Rows,
		}
		if wantTotal {
			n, cerr := op.Count(r.Context(), opts)
			if cerr == nil {
				total := n
				resp.Total = &total
			}
			// If Count fails (driver lacks support, permission, etc.)
			// we silently omit the field — clients fall back to the
			// page-count display.
		}
		setAuditRows(r.Context(), int64(len(res.Rows)))
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleInsertRow(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		schemaName := r.PathValue("s")
		table := r.PathValue("t")
		setAuditAction(r.Context(), dbadmin.ActionRowWrite, dbadmin.Target{ConnectionID: connID, Schema: schemaName, Object: table})
		if err := schema.ValidateIdentifier(schemaName); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if err := schema.ValidateIdentifier(table); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionRowWrite)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		var in insertRowRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if len(in.Values) == 0 {
			writeError(w, r, http.StatusBadRequest, CodeEmptyUpdate, "values required")
			return
		}
		conn, err := openConn(s, r.Context(), c)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		defer conn.Close()
		rdr, err := schema.For(conn, c.Engine)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		op, err := rows.New(conn, rdr, rows.Options{})
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		res, err := op.Insert(r.Context(), rows.InsertOpts{
			Schema: schemaName, Table: table, Values: in.Values,
		})
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		setAuditRows(r.Context(), res.RowsAffected)
		writeJSON(w, http.StatusOK, updateResultResponse{
			RowsAffected: res.RowsAffected,
			LastInsertID: res.LastInsertID,
		})
	}
}

func handleUpdateRow(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		schemaName := r.PathValue("s")
		table := r.PathValue("t")
		pkRaw := r.PathValue("pk")
		setAuditAction(r.Context(), dbadmin.ActionRowWrite, dbadmin.Target{ConnectionID: connID, Schema: schemaName, Object: table})
		if err := schema.ValidateIdentifier(schemaName); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if err := schema.ValidateIdentifier(table); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		pkMap, err := parsePK(pkRaw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, CodePKMismatch, err.Error())
			return
		}
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionRowWrite)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		var in updateRowRequest
		if err := readJSON(w, r, &in, 1<<20); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if len(in.Set) == 0 {
			writeError(w, r, http.StatusBadRequest, CodeEmptyUpdate, "set required")
			return
		}
		conn, err := openConn(s, r.Context(), c)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		defer conn.Close()
		rdr, err := schema.For(conn, c.Engine)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		op, err := rows.New(conn, rdr, rows.Options{})
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		res, err := op.UpdateByPK(r.Context(), rows.UpdateByPKOpts{
			Schema: schemaName, Table: table, PK: pkMap, Set: in.Set,
			// edit-1: optimistic-concurrency snapshot, optional.
			Where: in.Where,
		})
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		setAuditRows(r.Context(), res.RowsAffected)
		writeJSON(w, http.StatusOK, updateResultResponse{
			RowsAffected: res.RowsAffected,
		})
	}
}

func handleDeleteRow(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		schemaName := r.PathValue("s")
		table := r.PathValue("t")
		pkRaw := r.PathValue("pk")
		setAuditAction(r.Context(), dbadmin.ActionRowWrite, dbadmin.Target{ConnectionID: connID, Schema: schemaName, Object: table})
		if err := schema.ValidateIdentifier(schemaName); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if err := schema.ValidateIdentifier(table); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		pkMap, err := parsePK(pkRaw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, CodePKMismatch, err.Error())
			return
		}
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionRowWrite)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		conn, err := openConn(s, r.Context(), c)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		defer conn.Close()
		rdr, err := schema.For(conn, c.Engine)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		op, err := rows.New(conn, rdr, rows.Options{})
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		res, err := op.DeleteByPK(r.Context(), rows.DeleteByPKOpts{
			Schema: schemaName, Table: table, PK: pkMap,
		})
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		setAuditRows(r.Context(), res.RowsAffected)
		writeJSON(w, http.StatusOK, updateResultResponse{
			RowsAffected: res.RowsAffected,
		})
	}
}

// parseFilter parses "col:op:value" into a rows.Predicate.
//
// For OpIn / OpNotIn the value is JSON-decoded into a []any so the
// downstream rows.Predicate has the slice shape that rows.buildSelect
// requires (see WIRE-03). For all other ops the value is kept verbatim
// as a string and the driver coerces during parameter binding.
func parseFilter(spec string) (rows.Predicate, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) < 2 {
		return rows.Predicate{}, errors.New("filter must be col:op[:value]")
	}
	col := parts[0]
	op := rows.Op(parts[1])
	p := rows.Predicate{Column: col, Op: op}
	if len(parts) == 3 {
		raw := parts[2]
		if op == rows.OpIn || op == rows.OpNotIn {
			// WIRE-03: client sends a JSON-encoded array. Decode it
			// into a []any so the rows package can rebuild the
			// parameterized IN ($1,$2,…) tuple.
			var arr []any
			if err := json.Unmarshal([]byte(raw), &arr); err != nil {
				return rows.Predicate{}, fmt.Errorf("IN value must be a JSON array: %w", err)
			}
			if len(arr) == 0 {
				return rows.Predicate{}, errors.New("IN value cannot be empty")
			}
			p.Value = arr
		} else {
			p.Value = raw
		}
	}
	return p, nil
}

// parsePK parses the URL-path PK segment. Supported forms:
//
//	"42"             — single column; value is the string; caller's
//	                   table has exactly one PK column.
//	"col=val"        — single explicit column.
//	"a=1,b=2"        — composite.
//
// Values are kept as strings; the driver typically coerces.
func parsePK(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, errors.New("empty PK")
	}
	out := map[string]any{}
	if !strings.Contains(raw, "=") {
		out["__pk__"] = raw
		return out, nil
	}
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("bad PK fragment %q", part)
		}
		if err := schema.ValidateIdentifier(kv[0]); err != nil {
			return nil, err
		}
		out[kv[0]] = kv[1]
	}
	return out, nil
}
