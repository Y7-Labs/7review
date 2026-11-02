package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/cmd/7review/operator"
)

func TestCanonicalOperatorClientAuthenticatesAndReconcilesLostHTTPAcknowledgment(t *testing.T) {
	s, db, dsn := inboxServerDSN(t)
	s.cfg.Operators = appOperators(t)
	s.cfg.APIToken = "legacy-service-secret"
	runtime, err := accounting.OpenRuntime(context.Background(), accounting.RuntimeSettings{DatabaseURL: dsn, BoundaryID: "inbox-test", ProjectID: "team", Operators: s.cfg.Operators}, s.cfg.PolicyRuntimeCeilings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	s.accountingRuntime.Authority = runtime.Authority
	accepted := inboxWebhook(t, s, inboxBody, "operator-client")
	if accepted.Code != 202 {
		t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
	}
	var admission requestReviewResult
	if err = json.Unmarshal(accepted.Body.Bytes(), &admission); err != nil {
		t.Fatal(err)
	}
	s.mux = http.NewServeMux()
	s.routes()
	var drop atomic.Bool
	drop.Store(true)
	var posts, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/operator/attempt" {
			reads.Add(1)
		}
		if r.URL.Path == "/operator/commands" {
			posts.Add(1)
			if drop.CompareAndSwap(true, false) {
				recorder := httptest.NewRecorder()
				s.mux.ServeHTTP(recorder, r)
				if recorder.Code != 200 {
					t.Errorf("ack lost before successful commit: %d %s", recorder.Code, recorder.Body.String())
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
		}
		s.mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("REVIEW_API_TOKEN", "legacy-service-secret")
	t.Setenv("REVIEW_OPERATOR_TOKEN", "ALICE_TOKEN-secret")
	client := operator.NewClient(server.URL, server.Client())
	ctx := context.Background()
	v, err := client.Attempt(ctx, admission.AttemptID)
	if err != nil || v.ControlVersion != 1 || v.Revision != "head" {
		t.Fatalf("actual status contract: %+v %v", v, err)
	}
	c := review.CancelAttemptCommand{SchemaVersion: 1, CommandID: "client-cancel", AttemptID: v.AttemptID, ExpectedVersion: v.ControlVersion, Revision: v.Revision, Kind: "cancel_attempt", Payload: review.CancelAttemptPayload{Reason: "operator stop"}}
	_, err = client.CancelAttempt(ctx, c)
	var failure *operator.CanonicalRequestError
	if !errors.As(err, &failure) || !failure.MayHaveCommitted || posts.Load() != 1 {
		t.Fatalf("lost ack was retried/acknowledged: %v posts=%d", err, posts.Load())
	}
	r, err := client.CancelAttempt(ctx, c)
	if err != nil || r.ActorID != "alice" || r.ControlVersion != 2 || posts.Load() != 2 {
		t.Fatalf("exact command replay: %+v %v posts=%d", r, err, posts.Load())
	}
	var commands int
	var version uint64
	if err = db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),control_version FROM budget_jobs WHERE run_id=$1`, admission.RunID).Scan(&commands, &version); err != nil || commands != 1 || version != 2 {
		t.Fatalf("retry duplicated disposition: %d %d %v", commands, version, err)
	}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "BOB_TOKEN-secret")
	if _, err = client.Attempt(ctx, admission.AttemptID); !errors.As(err, &failure) || failure.StatusCode != 403 {
		t.Fatalf("wrong repository credential: %v", err)
	}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "ALICE_TOKEN-secret")
	current, err := s.requestReview(ctx, review.Request{Provider: "github", ProjectID: "org/repo", Repository: "org/repo", ChangeID: "7"})
	if err != nil || current.AttemptID == admission.AttemptID {
		t.Fatalf("fresh explicit intent: %+v %v", current, err)
	}
	c.CommandID = "stale-client-cancel"
	before := reads.Load()
	if _, err = client.CancelAttempt(ctx, c); !errors.As(err, &failure) || failure.StatusCode != 409 || failure.Conflict == nil || failure.Conflict.CurrentAttemptID != current.AttemptID || reads.Load() != before {
		t.Fatalf("stale target was hidden/retargeted: %v", err)
	}
}
