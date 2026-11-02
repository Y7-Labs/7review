package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/review"
)

// Canonical operator credentials must be unambiguous; the legacy API guard's
// empty-token compatibility bypass is deliberately not used here.
func operatorCredential(r *http.Request) string {
	auth, legacy := r.Header.Values("Authorization"), r.Header.Values("X-7review-Token")
	if len(auth) > 1 || len(legacy) > 1 {
		return ""
	}
	token := ""
	if len(auth) == 1 {
		if len(auth[0]) <= 7 || !strings.EqualFold(auth[0][:7], "Bearer ") {
			return ""
		}
		token = auth[0][7:]
	}
	if len(legacy) == 1 {
		if len(auth) == 1 && token != legacy[0] {
			return ""
		}
		token = legacy[0]
	}
	return token
}

func (s *Server) handleOperatorAttempt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var directory *review.OperatorDirectory
	if s != nil && s.cfg != nil {
		directory = s.cfg.Operators
	}
	actor, err := directory.Authenticate(operatorCredential(r))
	if err != nil {
		http.Error(w, "operator authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(r.URL.RawQuery) > 4096 {
		http.Error(w, "operator query too large", http.StatusBadRequest)
		return
	}
	// ParseQuery reports malformed escapes instead of silently dropping fields.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["attempt_id"]) != 1 || query.Get("attempt_id") == "" {
		http.Error(w, "exact attempt_id query required", http.StatusBadRequest)
		return
	}
	if r.Body != nil {
		var probe [1]byte
		n, e := io.ReadFull(r.Body, probe[:])
		if n != 0 || (e != nil && e != io.EOF) {
			http.Error(w, "request body is not allowed", http.StatusBadRequest)
			return
		}
	}
	if s.accountingRuntime == nil || s.accountingRuntime.Authority == nil {
		http.Error(w, "durable operator authority unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	v, err := s.accountingRuntime.Authority.OperatorAttempt(ctx, actor, query.Get("attempt_id"))
	if err != nil {
		status := http.StatusServiceUnavailable
		switch {
		case errors.Is(err, review.ErrOperatorUnauthorized):
			status = http.StatusUnauthorized
		case errors.Is(err, review.ErrOperatorForbidden):
			status = http.StatusForbidden
		case errors.Is(err, accounting.ErrOperatorAttemptNotFound):
			status = http.StatusNotFound
		case errors.Is(err, accounting.ErrOperatorAttemptInvalid):
			status = http.StatusBadRequest
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
