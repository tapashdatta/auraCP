package httpapi

import (
	"net/http"
	"time"

	"github.com/auracp/auracp/pkg/dbadmin"
)

func handleStepUpInitiate(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAuditAction(r.Context(), dbadmin.ActionConnPwdView, dbadmin.Target{})
		var in stepUpInitiateRequest
		if err := readJSON(w, r, &in, 1<<18); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if in.Action == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "action required")
			return
		}
		writeJSON(w, http.StatusOK, stepUpInitiateResponse{
			JTI:         newRequestID(),
			DeliveredBy: "totp",
			Expires:     time.Now().Add(2 * time.Minute),
		})
	}
}

func handleStepUpVerify(s *server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setAuditAction(r.Context(), dbadmin.ActionConnPwdView, dbadmin.Target{})
		var in stepUpVerifyRequest
		if err := readJSON(w, r, &in, 1<<18); err != nil {
			writeMappedErr(w, r, err)
			return
		}
		if in.JTI == "" || in.Assertion == "" {
			writeError(w, r, http.StatusBadRequest, CodeInvalidInput, "jti and assertion required")
			return
		}
		action, ttl, err := s.engine.AuthSurface().VerifyStepUp(r)
		if err != nil {
			writeError(w, r, http.StatusUnauthorized, CodeUnauthenticated, "step-up verification failed")
			return
		}
		writeJSON(w, http.StatusOK, stepUpVerifyResponse{
			Success:       true,
			GrantedAction: string(action),
			Expires:       time.Now().Add(ttl),
		})
	}
}
