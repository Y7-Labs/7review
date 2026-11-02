package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/orchestrator"
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

func TestCanonicalPublicationAuthorizationHTTPUsesTerminalEvidenceAndMappedReviewer(t *testing.T) {
	s, db, _ := inboxServerDSN(t)
	directory, err := review.DecodeOperatorDirectory([]byte(`{"schema_version":1,"principals":[{"id":"reviewer","token_env":"REVIEWER_TOKEN","grants":[{"boundary_id":"inbox-test","project_id":"team","provider":"github","host":"github.com","repository_id":"org/repo","roles":["reviewer"]}]},{"id":"participant","token_env":"PARTICIPANT_TOKEN","grants":[{"boundary_id":"inbox-test","project_id":"team","provider":"github","host":"github.com","repository_id":"org/repo","roles":["participant"]}]}]}`), func(name string) string { return strings.ToLower(strings.TrimSuffix(name, "_TOKEN")) + "-secret" })
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Operators = directory
	accepted := inboxWebhook(t, s, inboxBody, "publication-client")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
	}
	var admission reviewDispatchResult
	if err = json.Unmarshal(accepted.Body.Bytes(), &admission); err != nil {
		t.Fatal(err)
	}
	limits := s.cfg.PolicyRuntimeCeilings.Clone()
	catalog, err := accounting.NewCatalog([]accounting.RouteBound{{Provider: "aggregate-fixture", Model: "pinned", Version: "test-v1", Currency: "USD", EvidenceRef: "test:bounded-publication-provider", ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), InputHardLimit: 20000, OutputHardLimit: 10000, InputMicroPerMillion: 1, OutputMicroPerMillion: 1, AllOutputBounded: true, MaxActiveMS: 5000, NativeToolsQualified: true}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := accounting.NewAuthority(db, accounting.Config{RequireServiceJobs: true, BoundaryID: "inbox-test", ProjectID: "team", Limits: limits, Catalog: catalog, Operators: directory})
	if err != nil {
		t.Fatal(err)
	}
	s.accountingRuntime.Authority = authority
	s.pipeline.Canonical.Authority = authority
	provider := &aggregateQualifiedProvider{t: t, db: db, attempt: admission.AttemptID}
	build := func() *orchestrator.Orchestrator {
		return orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("pinned", "pinned", provider.Name()), map[string]orchestrator.LLMProvider{provider.Name(): provider}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(authority))
	}
	s.pipeline.Orchestrator = build()
	item, err := authority.ClaimInbox(context.Background())
	if err != nil || item == nil {
		t.Fatalf("claim: %#v %v", item, err)
	}
	ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
	err = s.runInboxItem(ctx, item.Request, item.RunID)
	if err != nil && !errors.Is(err, orchestrator.ErrDurableContinuation) {
		t.Fatalf("canonical execution failed: %v", err)
	}
	s.startInboxWorkers()
	terminal := 0
	for until := time.Now().Add(8 * time.Second); time.Now().Before(until); {
		if err = db.QueryRow(`SELECT count(*) FROM budget_jobs WHERE run_id=$1 AND terminal_reduction_id IS NOT NULL`, admission.RunID).Scan(&terminal); err != nil {
			t.Fatal(err)
		}
		if terminal == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.inboxCancel()
	s.inboxWorkers.Wait()
	if terminal != 1 {
		t.Fatal("validated terminal assessment was not completed")
	}
	projection, err := authority.LookupModelJob(context.Background(), admission.RunID)
	if err != nil || !projection.Succeeded || projection.Source.Report.Draft == "" {
		t.Fatalf("terminal projection: %#v %v", projection, err)
	}
	var assessmentDigest string
	if err = db.QueryRow(`SELECT m.artifact_digest FROM budget_jobs j JOIN budget_model_reductions m ON m.id=j.terminal_reduction_id WHERE j.run_id=$1`, admission.RunID).Scan(&assessmentDigest); err != nil {
		t.Fatal(err)
	}
	s.mux = http.NewServeMux()
	s.routes()
	server := httptest.NewServer(s.mux)
	defer server.Close()
	client := operator.NewClient(server.URL, server.Client())
	t.Setenv("REVIEW_OPERATOR_TOKEN", "reviewer-secret")
	view, err := client.Attempt(context.Background(), admission.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	draftDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(projection.Source.Report.Draft)))
	command := review.AuthorizePublicationCommand{SchemaVersion: 1, CommandID: "publish-http", AttemptID: admission.AttemptID, ExpectedVersion: view.ControlVersion, Revision: view.Revision, Kind: "authorize_publication", Payload: review.AuthorizePublicationPayload{AssessmentDigest: assessmentDigest, ArtifactDigest: draftDigest, Destination: review.PublicationDestination{Provider: "github", Host: "github.com", RepositoryID: "org/repo", ChangeNumber: "7", ArtifactClass: "assessment"}, Reason: "reviewed exact terminal assessment"}}
	beforeCalls := provider.calls.Load()
	t.Setenv("REVIEW_OPERATOR_TOKEN", "participant-secret")
	if _, err = client.AuthorizePublication(context.Background(), command); err == nil {
		t.Fatal("participant authorized publication")
	} else {
		var failure *operator.CanonicalRequestError
		if !errors.As(err, &failure) || failure.StatusCode != http.StatusForbidden {
			t.Fatalf("participant rejection: %v", err)
		}
	}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "reviewer-secret")
	stale := command
	stale.ExpectedVersion--
	stale.CommandID = "publish-http-stale"
	if _, err = client.AuthorizePublication(context.Background(), stale); err == nil {
		t.Fatal("stale publication authorization accepted")
	} else {
		var failure *operator.CanonicalRequestError
		if !errors.As(err, &failure) || failure.StatusCode != http.StatusConflict {
			t.Fatalf("stale rejection: %v", err)
		}
	}
	receipt, err := client.AuthorizePublication(context.Background(), command)
	if err != nil || receipt.ActorID != "reviewer" || receipt.State != "authorized" || receipt.ControlVersion != view.ControlVersion+1 {
		t.Fatalf("publication authorization: %+v %v", receipt, err)
	}
	if provider.calls.Load() != beforeCalls {
		t.Fatalf("authorization dispatched provider I/O: before=%d after=%d", beforeCalls, provider.calls.Load())
	}
}
