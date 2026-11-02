package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/review"
)

const appOperatorFixture = `{"schema_version":1,"principals":[{"id":"alice","token_env":"ALICE_TOKEN","grants":[{"boundary_id":"inbox-test","project_id":"team","provider":"github","host":"github.com","repository_id":"org/repo","roles":["operator"]}]},{"id":"bob","token_env":"BOB_TOKEN","grants":[{"boundary_id":"inbox-test","project_id":"team","provider":"github","host":"github.com","repository_id":"org/other","roles":["operator"]}]}]}`

func appOperators(t *testing.T) *review.OperatorDirectory {
	t.Helper()
	d, err := review.DecodeOperatorDirectory([]byte(appOperatorFixture), func(key string) string { return key + "-secret" })
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestOperatorAttemptFailsClosedWithoutExplicitPrincipal(t *testing.T) {
	s := &Server{cfg: &config.Config{APIToken: "service-token"}}
	for _, token := range []string{"", "service-token", "ALICE_TOKEN-secret"} {
		r := httptest.NewRequest(http.MethodGet, "/operator/attempt?attempt_id=attempt", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.handleOperatorAttempt(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unmapped credential %q: %d", token, w.Code)
		}
	}
	s.cfg.Operators = appOperators(t)
	for _, headers := range []http.Header{
		{"Authorization": {"Bearer ALICE_TOKEN-secret", "Bearer ALICE_TOKEN-secret"}},
		{"Authorization": {"Bearer ALICE_TOKEN-secret"}, "X-7review-Token": {"BOB_TOKEN-secret"}},
		{"Authorization": {"Bearer  ALICE_TOKEN-secret"}},
		{"X-7review-Token": {"ALICE_TOKEN-secret", "ALICE_TOKEN-secret"}},
	} {
		r := httptest.NewRequest(http.MethodGet, "/operator/attempt?attempt_id=attempt", nil)
		r.Header = headers
		w := httptest.NewRecorder()
		s.handleOperatorAttempt(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("ambiguous headers: %d", w.Code)
		}
	}
}

func TestOperatorAttemptFailsClosedWithoutDurableAuthority(t *testing.T) {
	s := &Server{cfg: &config.Config{Operators: appOperators(t)}}
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"attempt_id=attempt", 503},
		{"attempt_id=" + strings.Repeat("a", 4097), 400},
	} {
		r := httptest.NewRequest(http.MethodGet, "/operator/attempt?"+tc.query, nil)
		r.Header.Set("Authorization", "Bearer ALICE_TOKEN-secret")
		w := httptest.NewRecorder()
		s.handleOperatorAttempt(w, r)
		if w.Code != tc.status {
			t.Fatalf("missing authority/bound: %d expected %d", w.Code, tc.status)
		}
	}
}

func TestOperatorAttemptReadsOnlyAuthenticatedDurableRepository(t *testing.T) {
	s, _, dsn := inboxServerDSN(t)
	s.cfg.Operators = appOperators(t)
	runtime, err := accounting.OpenRuntime(context.Background(), accounting.RuntimeSettings{DatabaseURL: dsn, BoundaryID: "inbox-test", ProjectID: "team", Operators: s.cfg.Operators}, s.cfg.PolicyRuntimeCeilings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	s.accountingRuntime.Authority = runtime.Authority
	w := inboxWebhook(t, s, inboxBody, "operator-read")
	if w.Code != http.StatusAccepted {
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
	s.mux = http.NewServeMux()
	s.routes()
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{"?attempt_id=" + item.AttemptID, "ALICE_TOKEN-secret", 200},
		{"?attempt_id=" + item.AttemptID, "BOB_TOKEN-secret", 403},
		{"?attempt_id=" + item.RunID, "ALICE_TOKEN-secret", 404},
		{"?attempt_id=" + item.AttemptID + "&actor=alice", "BOB_TOKEN-secret", 400},
		{"?attempt_id=" + item.AttemptID + "&repository_id=org/other", "BOB_TOKEN-secret", 400},
		{"?attempt_id=" + item.AttemptID + "&attempt_id=other", "ALICE_TOKEN-secret", 400},
		{"?attempt_id=" + item.AttemptID + "&actor=%zz", "ALICE_TOKEN-secret", 400},
	} {
		r := httptest.NewRequest(http.MethodGet, "/operator/attempt"+tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var v accounting.OperatorAttemptView
			if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil || v.AttemptID != item.AttemptID || v.Revision != "head" || !v.Current {
				t.Fatalf("view: %+v %v", v, err)
			}
			if strings.Contains(w.Body.String(), "claim_token") || strings.Contains(w.Body.String(), "ALICE_TOKEN") {
				t.Fatal("authority secrets exposed")
			}
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/operator/attempt?attempt_id="+item.AttemptID, strings.NewReader(`{"actor":"alice"}`))
	r.Header.Set("Authorization", "Bearer ALICE_TOKEN-secret")
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("body claim accepted: %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/operator/attempt?attempt_id="+item.AttemptID, nil)
	r.Header.Set("Authorization", "Bearer ALICE_TOKEN-secret")
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != 405 {
		t.Fatalf("read endpoint accepted mutation method: %d", w.Code)
	}
	// The client used by existing CLI commands sends both identical headers.
	r = httptest.NewRequest(http.MethodGet, "/operator/attempt?attempt_id="+item.AttemptID, nil)
	r.Header.Set("Authorization", "Bearer ALICE_TOKEN-secret")
	r.Header.Set("X-7review-Token", "ALICE_TOKEN-secret")
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("identical CLI headers rejected: %d %s", w.Code, w.Body.String())
	}
}
