package api

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"

	"github.com/auracp/auracp/internal/files"
	"github.com/auracp/auracp/internal/logs"
	"github.com/auracp/auracp/internal/paths"
	"github.com/auracp/auracp/internal/store"
)

// partFilename returns the raw filename parameter from a multipart part's
// Content-Disposition header, WITHOUT running it through filepath.Base()
// the way Go's Part.FileName() does. We need the leading "subdir/" segments
// so the upload handler can recreate a dropped folder tree on disk.
func partFilename(p *multipart.Part) string {
	cd := p.Header.Get("Content-Disposition")
	if cd == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	// RFC 5987 prefers filename* but practical browsers always send filename
	// for File parts. The relPath is plain ASCII (we built it from JS
	// entry.name which the browser already normalised), so plain filename
	// is sufficient.
	if fn := params["filename"]; fn != "" {
		return fn
	}
	return ""
}

// GET /api/sites/{domain}/logs?kind=access&n=200
func (s *Server) siteLogs(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.SiteByDomain(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	n := 200
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
		n = v
	}
	lines, err := logs.Tail(st.SiteUser, r.URL.Query().Get("kind"), n)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}

// GET /api/sites/{domain}/files?path=sub/dir
func (s *Server) siteFiles(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	entries, err := files.List(root, r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// POST /api/sites/{domain}/files (multipart/form-data; path=<sub>, files=<...>)
//
// v0.2.19: streams the multipart body part-by-part directly to disk via
// MultipartReader, instead of the old ParseMultipartForm path that buffered
// up to 256 MB before spilling to /tmp. The new flow has no memory cap on
// upload size — it's bounded by available disk in the site's root — and
// surfaces partial-success per part the same way the old path did.
//
// Path field: we expect "path" to appear before "files" (FormData appends in
// DOM order; our SPA does this correctly). If "files" come first we treat
// path as empty (i.e. upload to the site home).
func (s *Server) uploadFiles(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected multipart/form-data: " + err.Error()})
		return
	}
	root := paths.SiteHome(st.SiteUser)
	sub := ""
	saved := 0
	var errs []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "multipart read failed: " + err.Error()})
			return
		}
		switch part.FormName() {
		case "path":
			// Form fields are small; read into memory with a tight cap.
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			sub = string(b)
		case "files":
			// v0.2.26: Go's Part.FileName() pipes the filename through
			// filepath.Base(), which strips any leading "subdir/" — exactly
			// what the folder-drop path relies on. Parse Content-Disposition
			// ourselves so the relative path survives.
			name := partFilename(part)
			if name == "" {
				_ = part.Close()
				continue
			}
			// SaveAt handles both flat (no slashes → basename → Save) and
			// nested (creates intermediate dirs first).
			if err := files.SaveAt(st.SiteUser, root, sub, name, part); err != nil {
				errs = append(errs, name+": "+err.Error())
			} else {
				saved++
			}
		}
		_ = part.Close()
	}
	if saved == 0 && len(errs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no files in the upload"})
		return
	}
	s.audit(r, "site.files.upload", domain+"/"+sub)
	writeJSON(w, http.StatusOK, map[string]any{"saved": saved, "errors": errs})
}

// GET /api/sites/{domain}/files/download?path=sub/file.ext
func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	sub := r.URL.Query().Get("path")
	f, fi, err := files.Open(root, sub)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fi.Name()+"\"")
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// DELETE /api/sites/{domain}/files?path=sub/file.ext
func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	sub := r.URL.Query().Get("path")
	if err := files.Delete(root, sub); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.delete", domain+"/"+sub)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/sites/{domain}/files/rename {"path": "old/sub", "newName": "newName"}
func (s *Server) renameFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		Path    string `json:"path"`
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	if err := files.Rename(root, in.Path, in.NewName); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.rename", domain+"/"+in.Path+"→"+in.NewName)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/sites/{domain}/files/mkdir {"path": "parent/sub", "name": "folder"}
func (s *Server) mkdirFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	if err := files.Mkdir(st.SiteUser, root, in.Path, in.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.mkdir", domain+"/"+in.Path+"/"+in.Name)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// POST /api/sites/{domain}/files/touch {"path": "parent/sub", "name": "file.ext"}
func (s *Server) touchFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	if err := files.Touch(st.SiteUser, root, in.Path, in.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.touch", domain+"/"+in.Path+"/"+in.Name)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// GET /api/sites/{domain}/files/text?path=sub/file.ext — read a text file for
// the in-browser editor. Refuses binary or files > 1 MiB.
func (s *Server) readTextFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	sub := r.URL.Query().Get("path")
	content, err := files.ReadText(root, sub)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

// POST /api/sites/{domain}/files/chmod {"path": "sub/file", "mode": "0644"}
// Mode is parsed as octal; the request fails if it's outside 0–0777.
func (s *Server) chmodFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	n, err := strconv.ParseUint(in.Mode, 8, 32)
	if err != nil || n > 0o777 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be octal 0–0777 (e.g. 0644)"})
		return
	}
	root := paths.SiteHome(st.SiteUser)
	if err := files.Chmod(root, in.Path, os.FileMode(n)); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.chmod", domain+"/"+in.Path+":"+in.Mode)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/sites/{domain}/files/delete-many {"paths": ["sub/a", "sub/b"]}
// Bulk delete is one round-trip; per-entry errors come back in `errors`.
func (s *Server) deleteManyFiles(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct{ Paths []string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	deleted := 0
	var errs []string
	for _, p := range in.Paths {
		if err := files.Delete(root, p); err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		deleted++
	}
	s.audit(r, "site.files.delete-many", domain+" ×"+strconv.Itoa(deleted))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "errors": errs})
}

// POST /api/sites/{domain}/files/zip {"paths": [...], "dest": "archive.zip"}
func (s *Server) zipFiles(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		Paths []string
		Dest  string
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	if err := files.Zip(st.SiteUser, root, in.Paths, in.Dest); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.zip", domain+"/"+in.Dest)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// POST /api/sites/{domain}/files/unzip {"path": "sub/archive.zip"}
func (s *Server) unzipFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct{ Path string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	if err := files.Unzip(st.SiteUser, root, in.Path); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.unzip", domain+"/"+in.Path)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PUT /api/sites/{domain}/files/text {"path": "sub/file.ext", "content": "..."}
func (s *Server) writeTextFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	if err := files.WriteText(st.SiteUser, root, in.Path, in.Content); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.edit", domain+"/"+in.Path)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// POST /api/sites/{domain}/files/clone {"path": "sub/file"}
func (s *Server) cloneFile(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct{ Path string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	root := paths.SiteHome(st.SiteUser)
	newName, err := files.Clone(root, st.SiteUser, in.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.files.clone", domain+"/"+in.Path)
	writeJSON(w, http.StatusOK, map[string]string{"name": newName})
}

// GET /api/sites/{domain}/cron
func (s *Server) listCron(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.CronJobsForSite(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if jobs == nil {
		jobs = []store.CronJob{}
	}
	writeJSON(w, http.StatusOK, jobs)
}

// POST /api/sites/{domain}/cron
func (s *Server) addCron(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	var in struct {
		Schedule string `json:"schedule"`
		Command  string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Schedule == "" || in.Command == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "schedule and command are required"})
		return
	}
	if err := s.store.AddCronJob(store.CronJob{
		Domain: domain, User: st.SiteUser, Schedule: in.Schedule, Command: in.Command, Enabled: true,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.cron.Apply(r.Context(), st.SiteUser, domain); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

// DELETE /api/sites/{domain}/cron/{id}
func (s *Server) deleteCron(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	st, err := s.store.SiteByDomain(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteCronJob(domain, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.cron.Apply(r.Context(), st.SiteUser, domain); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
