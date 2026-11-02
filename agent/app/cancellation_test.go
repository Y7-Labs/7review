package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/review"
)

func TestOperatorCancellationAuthenticatesScopesVersionsAndReplay(t *testing.T) {
	s, _, dsn := inboxServerDSN(t)
	s.cfg.Operators = appOperators(t)
	runtime, err := accounting.OpenRuntime(context.Background(), accounting.RuntimeSettings{DatabaseURL: dsn, BoundaryID: "inbox-test", ProjectID: "team", Operators: s.cfg.Operators}, s.cfg.PolicyRuntimeCeilings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	s.accountingRuntime.Authority = runtime.Authority
	w := inboxWebhook(t, s, inboxBody, "operator-cancel")
	if w.Code != 202 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	var accepted requestReviewResult
	if err = json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	item, err := runtime.Authority.LookupInbox(context.Background(), accepted.RunID)
	if err != nil {
		t.Fatal(err)
	}
	c := review.CancelAttemptCommand{SchemaVersion: 1, CommandID: "cancel-http", AttemptID: item.AttemptID, ExpectedVersion: 1, Revision: "head", Kind: "cancel_attempt", Payload: review.CancelAttemptPayload{Reason: "stop review"}}
	raw, _ := json.Marshal(c)
	s.mux = http.NewServeMux()
	s.routes()
	post := func(body []byte, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/operator/commands", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		return w
	}
	for _, token := range []string{"", "service-token", "BOB_TOKEN-secret"} {
		want := 401
		if token == "BOB_TOKEN-secret" {
			want = 403
		}
		w := post(raw, token)
		if w.Code != want {
			t.Fatalf("credential %q: %d %s", token, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{strings.Replace(string(raw), `"kind":"cancel_attempt"`, `"kind":"cancel_attempt","actor":"alice"`, 1), strings.Replace(string(raw), `"kind":"cancel_attempt"`, `"kind":"approve"`, 1)} {
		if w := post([]byte(body), "ALICE_TOKEN-secret"); w.Code != 400 {
			t.Fatalf("forged envelope: %d %s", w.Code, w.Body.String())
		}
	}
	stale := c
	stale.Revision = "old"
	staleRaw, _ := json.Marshal(stale)
	w = post(staleRaw, "ALICE_TOKEN-secret")
	if w.Code != 409 || !strings.Contains(w.Body.String(), item.AttemptID) || !strings.Contains(w.Body.String(), "current_attempt_url") {
		t.Fatalf("stale revision: %d %s", w.Code, w.Body.String())
	}
	w = post(raw, "ALICE_TOKEN-secret")
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	var receipt review.CancelAttemptReceipt
	if err = json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || receipt.ActorID != "alice" || receipt.ControlVersion != 2 || receipt.ReservationDisposition != "retained" {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if replay := post(raw, "ALICE_TOKEN-secret"); replay.Code != 200 || replay.Body.String() != w.Body.String() {
		t.Fatalf("lost response replay: %d %s", replay.Code, replay.Body.String())
	}
	c.Payload.Reason = "different"
	changed, _ := json.Marshal(c)
	if w := post(changed, "ALICE_TOKEN-secret"); w.Code != 409 {
		t.Fatalf("ID conflict: %d %s", w.Code, w.Body.String())
	}
	if w := post([]byte(strings.Repeat("x", review.MaxOperatorCommandBytes+1)), "ALICE_TOKEN-secret"); w.Code != 413 {
		t.Fatalf("unbounded body: %d", w.Code)
	}
	item, err = runtime.Authority.LookupInbox(context.Background(), item.RunID)
	if err != nil || item.State != "cancelled" || item.ClaimToken != "" {
		t.Fatalf("cancelled inbox: %+v %v", item, err)
	}
}
