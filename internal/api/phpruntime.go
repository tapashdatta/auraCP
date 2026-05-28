package api

import (
	"encoding/json"
	"net/http"
)

// PHP runtime + per-site PHP settings handlers, mirroring noderuntime.go.

func (s *Server) listPHPRuntimes(w http.ResponseWriter, r *http.Request) {
	rts, err := s.store.PHPRuntimes()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Decorate with `installed` (truthy when the package is on disk) so the UI
	// can grey-out "Set default" for versions the operator only enabled in DB.
	installed := map[string]bool{}
	if s.php != nil {
		for _, v := range s.php.Installed() {
			installed[v] = true
		}
	}
	type view struct {
		Version   string `json:"version"`
		IsDefault bool   `json:"isDefault"`
		Installed bool   `json:"installed"`
	}
	out := make([]view, 0, len(rts))
	for _, r := range rts {
		out = append(out, view{Version: r.Version, IsDefault: r.IsDefault, Installed: installed[r.Version]})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) installPHPRuntime(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version   string `json:"version"`
		AsDefault bool   `json:"asDefault"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if s.php == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoPHPManager)
		return
	}
	if err := s.php.Install(r.Context(), in.Version, in.AsDefault); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "php.install", in.Version)
	writeJSON(w, http.StatusCreated, map[string]string{"version": in.Version})
}

func (s *Server) setDefaultPHPRuntime(w http.ResponseWriter, r *http.Request) {
	v := r.PathValue("version")
	if s.php == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoPHPManager)
		return
	}
	if err := s.php.SetDefault(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "php.default", v)
	writeJSON(w, http.StatusOK, map[string]string{"version": v})
}

func (s *Server) deletePHPRuntime(w http.ResponseWriter, r *http.Request) {
	v := r.PathValue("version")
	if s.php == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoPHPManager)
		return
	}
	if err := s.php.Remove(r.Context(), v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "php.remove", v)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setSitePHPVersion(w http.ResponseWriter, r *http.Request) {
	var in struct{ Version string `json:"version"` }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	domain := r.PathValue("domain")
	if err := s.store.SetSitePHPVersion(domain, in.Version); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Rewriting the pool config + reloading the right fpm service happens via
	// sites.ReapplyRuntime → phpruntime.WritePool.
	if err := s.reapplyRuntime(r.Context(), domain); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.php-version", domain+"="+in.Version)
	writeJSON(w, http.StatusOK, map[string]string{"version": in.Version})
}

func (s *Server) getPHPSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.PHPSettings(r.PathValue("domain"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) setPHPSettings(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	for k, v := range in {
		if v == "" {
			_ = s.store.DeletePHPSetting(domain, k)
		} else {
			_ = s.store.SetPHPSetting(domain, k, v)
		}
	}
	if err := s.reapplyRuntime(r.Context(), domain); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "site.php-settings", domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listCertificates(w http.ResponseWriter, r *http.Request) {
	cs, err := s.store.Certificates()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) renewCertificate(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if s.acme == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoACMEManager)
		return
	}
	if err := s.acme.IssueOnce(r.Context(), domain); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r, "cert.renew", domain)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Sentinel errors so the PHP-runtime endpoints can degrade gracefully on hosts
// where the package isn't installed yet (panel up, data plane not provisioned).
var (
	errNoPHPManager  = simpleErr("PHP runtime manager not initialised on this host")
	errNoACMEManager = simpleErr("ACME manager not initialised on this host")
)

type simpleErr string

func (e simpleErr) Error() string { return string(e) }
