package httpapi

import (
	"errors"
	"net/http"

	"github.com/auracp/auracp/pkg/dbadmin"
	"github.com/auracp/auracp/pkg/dbadmin/schema"
)

func handleListSchemas(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		setAuditAction(r.Context(), dbadmin.ActionSchemaRead, dbadmin.Target{ConnectionID: connID})
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionSchemaRead)
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
		dbs, err := rdr.ListDatabases(r.Context())
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, listSchemasResponse{Schemas: dbs})
	}
}

func handleListObjects(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		schemaName := r.PathValue("s")
		setAuditAction(r.Context(), dbadmin.ActionSchemaRead, dbadmin.Target{ConnectionID: connID, Schema: schemaName})
		if err := schema.ValidateIdentifier(schemaName); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionSchemaRead)
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
		tables, err := rdr.ListTables(r.Context(), schemaName)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		views, _ := rdr.ListViews(r.Context(), schemaName)
		funcs, _ := rdr.ListFunctions(r.Context(), schemaName)
		procs, _ := rdr.ListProcedures(r.Context(), schemaName)
		out := listObjectsResponse{
			Tables:     make([]tableSummaryDTO, 0, len(tables)),
			Views:      make([]viewSummaryDTO, 0, len(views)),
			Functions:  make([]functionSummaryDTO, 0, len(funcs)),
			Procedures: make([]procedureSummaryDTO, 0, len(procs)),
		}
		for _, t := range tables {
			out.Tables = append(out.Tables, tableSummaryDTO{
				Schema: t.Schema, Name: t.Name, Kind: t.Kind.String(),
				Comment: t.Comment, RowsEstimate: t.RowsEstimate, Engine: t.Engine,
			})
		}
		for _, v := range views {
			out.Views = append(out.Views, viewSummaryDTO{
				Schema: v.Schema, Name: v.Name, Comment: v.Comment,
				Updatable: v.Updatable, Definition: v.Definition,
			})
		}
		for _, f := range funcs {
			out.Functions = append(out.Functions, functionSummaryDTO{
				Schema: f.Schema, Name: f.Name, Language: f.Language,
				ReturnType: f.ReturnType, Arguments: f.Arguments,
				Comment: f.Comment, IsAggregate: f.IsAggregate,
			})
		}
		for _, p := range procs {
			out.Procedures = append(out.Procedures, procedureSummaryDTO{
				Schema: p.Schema, Name: p.Name, Language: p.Language,
				Arguments: p.Arguments, Comment: p.Comment,
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func handleGetTable(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		connID := dbadmin.ConnectionID(r.PathValue("id"))
		schemaName := r.PathValue("s")
		table := r.PathValue("t")
		setAuditAction(r.Context(), dbadmin.ActionSchemaRead, dbadmin.Target{ConnectionID: connID, Schema: schemaName, Object: table})
		if err := schema.ValidateIdentifier(schemaName); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if err := schema.ValidateIdentifier(table); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		user, _ := userFrom(r.Context())
		c, err := resolveConnection(s, r.Context(), user, connID, dbadmin.ActionSchemaRead)
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
		t, err := rdr.GetTable(r.Context(), schemaName, table)
		if err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if t == nil {
			writeMappedErr(w, r, errors.New("schema: nil table"))
			return
		}
		writeJSON(w, http.StatusOK, tableToDTO(t))
	}
}
