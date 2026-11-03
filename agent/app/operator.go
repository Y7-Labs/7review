package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
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

func (s *Server) operatorActor(r *http.Request) (review.OperatorActor, error) {
	var directory *review.OperatorDirectory
	if s != nil && s.cfg != nil {
		directory = s.cfg.Operators
	}
	return directory.Authenticate(operatorCredential(r))
}

func (s *Server) handleOperatorAttempt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, err := s.operatorActor(r)
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

func (s *Server) handleOperatorCommand(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, err := s.operatorActor(r)
	if err != nil {
		http.Error(w, "operator authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "operator commands do not accept query claims", http.StatusBadRequest)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
		return
	}
	if r.Body == nil {
		http.Error(w, "command required", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, review.MaxOperatorCommandBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	var cancelCommand *review.CancelAttemptCommand
	var publicationCommand *review.AuthorizePublicationCommand
	var dispositionCommand *review.FindingDispositionCommand
	if c, decodeErr := review.DecodeCancelAttemptCommand(data); decodeErr == nil {
		cancelCommand = &c
	} else if c, decodeErr := review.DecodeAuthorizePublicationCommand(data); decodeErr == nil {
		publicationCommand = &c
	} else if c, decodeErr := review.DecodeFindingDispositionCommand(data); decodeErr == nil {
		dispositionCommand = &c
	} else {
		http.Error(w, "invalid operator command envelope", http.StatusBadRequest)
		return
	}
	if s.accountingRuntime == nil || s.accountingRuntime.Authority == nil {
		http.Error(w, "durable operator authority unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var receipt any
	if cancelCommand != nil {
		receipt, err = s.accountingRuntime.Authority.CancelAttempt(ctx, actor, *cancelCommand)
	} else if publicationCommand != nil {
		receipt, err = s.accountingRuntime.Authority.AuthorizePublication(ctx, actor, *publicationCommand)
	} else {
		receipt, err = s.accountingRuntime.Authority.DispositionFinding(ctx, actor, *dispositionCommand)
	}
	if err != nil {
		var conflict *accounting.OperatorCommandConflict
		if errors.As(err, &conflict) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(struct {
				*accounting.OperatorCommandConflict
				CurrentAttemptURL string `json:"current_attempt_url"`
			}{conflict, "/operator/attempt?attempt_id=" + url.QueryEscape(conflict.CurrentAttemptID)})
			return
		}
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
	_ = json.NewEncoder(w).Encode(receipt)
}
