package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/internal/testpostgres"
)

func inboxServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	db, dsn := testpostgres.OpenDSN(t)
	if err := accounting.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	scope := review.ResourceLimitScope{ModelCalls: 10, ToolCalls: 10, InputTokens: 1000000, OutputTokens: 10000, MoneyMicro: 1000, ActiveMS: 10000, ObservationBytes: 1000000}
	period := &review.FixedBudgetPeriod{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 86400000}
	limits := review.ResourceLimits{Currency: "USD", Attempt: scope, Change: scope, Project: scope}
	limits.Change.Period = period
	limits.Project.Period = period
	runtime, err := accounting.OpenRuntime(context.Background(), accounting.RuntimeSettings{DatabaseURL: dsn, BoundaryID: "inbox-test", ProjectID: "team"}, &limits)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{WebhookWorkers: 2, WebhookJobTimeout: 5000, PolicyV2Mode: "enforce", Provider: "openai_compat", ProviderAPIKey: "test", ProviderBaseURL: "http://127.0.0.1:1", ReviewModel: "review", SmallModel: "small"}
	model, err := orchestrator.BuildOrchestrator(cfg, orchestrator.WithBudgetAuthority(runtime.Authority))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, accountingRuntime: runtime, work: make(chan workItem), pipeline: &pipeline.Pipeline{Config: cfg, Orchestrator: model, Jobs: pipeline.NewMemoryRunStore(), SCM: canonicalServiceSCM{}, TrustedPolicy: canonicalServicePolicy{limits}, Canonical: &pipeline.CanonicalIntake{Authority: runtime.Authority, ProviderHosts: map[string]string{"github": "github.com"}, Execution: review.ExecutionContext{Mode: review.ExecutionService, ProducerID: "7review-service", Persistence: review.PersistenceDurableService, TrustedBoundaryID: runtime.BoundaryID}}, DeliveryMode: pipeline.DeliveryArtifactOnly}}
	t.Cleanup(func() { s.Close() })
	return s, db
}

func inboxWebhook(t *testing.T, s *Server, body, delivery string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	r.Header.Set("X-GitHub-Event", "pull_request")
	r.Header.Set("X-GitHub-Delivery", delivery)
	r.Header.Set("X-Hub-Signature-256", githubSignature("secret", body))
	w := httptest.NewRecorder()
	gitHubWebhookHandler("secret", s.handleWebhookReview)(w, r)
	return w
}

const inboxBody = `{"action":"synchronize","number":7,"repository":{"full_name":"org/repo"},"pull_request":{"head":{"sha":"head"},"base":{"sha":"base"}}}`

func TestCanonicalHTTPNeverFallsBackToInMemoryAcceptance(t *testing.T) {
	s := &Server{pipeline: &pipeline.Pipeline{Canonical: &pipeline.CanonicalIntake{}}, work: make(chan workItem, 1)}
	if _, err := s.enqueueReview(review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7"}, true); err == nil {
		t.Fatal("canonical request accepted without durable inbox")
	}
	if len(s.work) != 0 {
		t.Fatal("canonical request fell back to volatile queue")
	}
}

func TestCanonicalHTTPAcknowledgesOnlyDurableAcceptanceAndReplaysStableID(t *testing.T) {
	s, db := inboxServer(t)
	w := inboxWebhook(t, s, inboxBody, "delivery")
	if w.Code != 202 {
		t.Fatalf("HTTP=%d %s", w.Code, w.Body.String())
	}
	var first reviewDispatchResult
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	var runID, state string
	if err := db.QueryRow(`SELECT run_id,state FROM budget_inbox`).Scan(&runID, &state); err != nil || runID != first.RunID || state != "pending" {
		t.Fatalf("202 without durable pending work: %s %s %v", runID, state, err)
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/inbox?run_id="+first.RunID, nil)
	statusResponse := httptest.NewRecorder()
	s.handleInbox(statusResponse, statusRequest)
	if statusResponse.Code != 200 || !strings.Contains(statusResponse.Body.String(), `"pending"`) || strings.Contains(statusResponse.Body.String(), "ClaimToken") {
		t.Fatalf("pending receipt not inspectable safely: %d %s", statusResponse.Code, statusResponse.Body.String())
	}
	// The in-memory queue has no capacity and no worker, but acceptance survives.
	replay := inboxWebhook(t, s, inboxBody, "delivery")
	var next reviewDispatchResult
	_ = json.Unmarshal(replay.Body.Bytes(), &next)
	if replay.Code != 202 || next.RunID != first.RunID {
		t.Fatalf("lost-response replay changed ID: %d %#v", replay.Code, next)
	}
	conflict := inboxWebhook(t, s, strings.Replace(inboxBody, `"synchronize"`, `"opened"`, 1), "delivery")
	if conflict.Code < 400 {
		t.Fatalf("payload conflict acknowledged: %d %s", conflict.Code, conflict.Body.String())
	}
	if _, err := db.Exec(`ALTER TABLE budget_inbox RENAME TO unavailable_inbox`); err != nil {
		t.Fatal(err)
	}
	unavailable := inboxWebhook(t, s, inboxBody, "new-delivery")
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("inbox outage HTTP=%d", unavailable.Code)
	}
}

func TestCanonicalInboxWorkerRecoversPendingButNeverReplaysClaimedRun(t *testing.T) {
	s, db := inboxServer(t)
	req := review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", SourceSHA: "head", TargetSHA: "base", DeliveryID: "pending"}
	pending, err := s.accountingRuntime.Authority.AcceptInbox(context.Background(), req, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req.DeliveryID = "crashed"
	crashed, err := s.accountingRuntime.Authority.AcceptInbox(context.Background(), req, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Mark the second row as claimed by a process that died after claiming.
	if _, err = db.Exec(`UPDATE budget_inbox SET state='running',claim_token='dead-worker' WHERE run_id=$1`, crashed.RunID); err != nil {
		t.Fatal(err)
	}
	s.startInboxWorkers()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var state string
		_ = db.QueryRow(`SELECT state FROM budget_inbox WHERE run_id=$1`, pending.RunID).Scan(&state)
		if state == "failed" || state == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = s.pipeline.Jobs.Get(context.Background(), pending.RunID); err != nil {
		t.Fatalf("pending acceptance not recovered: %v", err)
	}
	if _, err = s.pipeline.Jobs.Get(context.Background(), crashed.RunID); err == nil {
		t.Fatal("claimed acceptance was replayed after crash")
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM budget_inbox WHERE run_id=$1`, crashed.RunID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("uncertain ownership changed: %s %v", state, err)
	}
}

type inboxBlockingSCM struct{ started chan time.Time }

func (s inboxBlockingSCM) Enrich(ctx context.Context, _ review.Request) (*review.SCMContext, error) {
	deadline, _ := ctx.Deadline()
	s.started <- deadline
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCanonicalInboxShutdownPreservesClaimAndOriginalDeadline(t *testing.T) {
	s, db := inboxServer(t)
	started := make(chan time.Time, 1)
	s.pipeline.SCM = inboxBlockingSCM{started}
	req := review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "shutdown"}
	item, err := s.accountingRuntime.Authority.AcceptInbox(context.Background(), req, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.startInboxWorkers()
	select {
	case deadline := <-started:
		if !deadline.Equal(item.Deadline) {
			t.Fatalf("recovery reset deadline: %s != %s", deadline, item.Deadline)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM budget_inbox WHERE run_id=$1`, item.RunID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("shutdown fabricated completion or requeued uncertain work: %s %v", state, err)
	}
}

type inboxPanickingSCM struct{}

func (inboxPanickingSCM) Enrich(context.Context, review.Request) (*review.SCMContext, error) {
	panic("injected crash")
}

func TestCanonicalInboxPanicIsNotRecordedAsSafeFailure(t *testing.T) {
	s, db := inboxServer(t)
	s.pipeline.SCM = inboxPanickingSCM{}
	item, err := s.accountingRuntime.Authority.AcceptInbox(context.Background(), review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "panic"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	s.startInboxWorkers()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, err = s.pipeline.Jobs.Get(context.Background(), item.RunID); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM budget_inbox WHERE run_id=$1`, item.RunID).Scan(&state); err != nil || state != "running" {
		t.Fatalf("panic fabricated safe terminal outcome: %s %v", state, err)
	}
}
