package httpapi

import (
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

		res, err := op.Read(r.Context(), opts)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		setAuditRows(r.Context(), int64(len(res.Rows)))
		writeJSON(w, http.StatusOK, readRowsResponse{
			Columns: columnInfosToDTO(res.Columns),
			Rows:    res.Rows,
		})
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

// parseFilter parses "col:op:value" into a rows.Predicate. The "value"
// is decoded JSON to preserve type when possible; falls back to string.
func parseFilter(spec string) (rows.Predicate, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) < 2 {
		return rows.Predicate{}, errors.New("filter must be col:op[:value]")
	}
	col := parts[0]
	op := rows.Op(parts[1])
	p := rows.Predicate{Column: col, Op: op}
	if len(parts) == 3 {
		p.Value = parts[2]
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
