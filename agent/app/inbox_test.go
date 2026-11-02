package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
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
	s, db, _ := inboxServerDSN(t)
	return s, db
}

func inboxServerDSN(t *testing.T) (*Server, *sql.DB, string) {
	t.Helper()
	db, dsn := testpostgres.OpenDSN(t)
	if err := accounting.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return newInboxServer(t, dsn), db, dsn
}

func newInboxServer(t *testing.T, dsn string) *Server {
	t.Helper()
	scope := review.ResourceLimitScope{ModelCalls: 10, ToolCalls: 10, InputTokens: 1000000, OutputTokens: 10000, MoneyMicro: 1000, ActiveMS: 10000, ObservationBytes: 1000000}
	period := &review.FixedBudgetPeriod{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 86400000}
	limits := review.ResourceLimits{Currency: "USD", Attempt: scope, Change: scope, Project: scope}
	limits.Change.Period = period
	limits.Project.Period = period
	runtime, err := accounting.OpenRuntime(context.Background(), accounting.RuntimeSettings{DatabaseURL: dsn, BoundaryID: "inbox-test", ProjectID: "team"}, &limits)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{WebhookWorkers: 2, WebhookJobTimeout: 5000, PolicyV2Mode: "enforce", PolicyRuntimeCeilings: &limits, Provider: "openai_compat", ProviderAPIKey: "test", ProviderBaseURL: "http://127.0.0.1:1", ReviewModel: "review", SmallModel: "small"}
	model, err := orchestrator.BuildOrchestrator(cfg, orchestrator.WithBudgetAuthority(runtime.Authority))
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: cfg, accountingRuntime: runtime, work: make(chan workItem), pipeline: &pipeline.Pipeline{Config: cfg, Orchestrator: model, Jobs: pipeline.NewMemoryRunStore(), SCM: canonicalServiceSCM{}, TrustedPolicy: canonicalServicePolicy{limits}, Canonical: &pipeline.CanonicalIntake{RequireJob: true, Authority: runtime.Authority, ProviderHosts: map[string]string{"github": "github.com"}, Execution: review.ExecutionContext{Mode: review.ExecutionService, ProducerID: "7review-service", Persistence: review.PersistenceDurableService, TrustedBoundaryID: runtime.BoundaryID}}, DeliveryMode: pipeline.DeliveryArtifactOnly}}
	t.Cleanup(func() { s.Close() })
	return s
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

type crashAfterPreparation struct{ *accounting.Authority }

func (a crashAfterPreparation) PrepareAction(ctx context.Context, i orchestrator.DispatchIntent) (orchestrator.PreparedAction, error) {
	p, err := a.Authority.PrepareAction(ctx, i)
	if err == nil {
		panic("process lost after durable preparation")
	}
	return p, err
}

func TestInboxAutomaticallyRecoversPreparedActionWithoutReplayingPipeline(t *testing.T) {
	exercisePreparedActionRecovery(t, false)
}

func TestInboxAutomaticallyContinuesRecoveredFindingsRepairWithoutPipelineReplay(t *testing.T) {
	exercisePreparedActionRecovery(t, true)
}

func exercisePreparedActionRecovery(t *testing.T, repair bool) {
	exerciseModelContinuation(t, repair, true, false)
}

func TestInboxLiveFormatterHandsOffToSameDurableChildAsRecovery(t *testing.T) {
	exerciseModelContinuation(t, true, false, false)
}

func TestInboxLiveAndRecoveryRaceReuseOneFormatterChild(t *testing.T) {
	exerciseModelContinuation(t, true, false, true)
}

type pausedContinuationAuthority struct {
	*accounting.Authority
	ready, release chan struct{}
	once           sync.Once
}

func (a *pausedContinuationAuthority) AdvanceModelContinuation(ctx context.Context, id string, builder orchestrator.ModelContinuationBuilder) (orchestrator.PreparedAction, error) {
	a.once.Do(func() { close(a.ready) })
	select {
	case <-ctx.Done():
		return orchestrator.PreparedAction{}, ctx.Err()
	case <-a.release:
	}
	return a.Authority.AdvanceModelContinuation(ctx, id, builder)
}

func exerciseModelContinuation(t *testing.T, repair, crash, race bool) {
	s, db, dsn := inboxServerDSN(t)
	w := inboxWebhook(t, s, inboxBody, "prepared-restart")
	if w.Code != 202 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	var receipt reviewDispatchResult
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	limits := s.cfg.PolicyRuntimeCeilings.Clone()
	catalog, err := accounting.NewCatalog([]accounting.RouteBound{{Provider: "aggregate-fixture", Model: "pinned", Version: "test-v1", Currency: "USD", EvidenceRef: "test:bounded-in-memory-provider", ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), InputHardLimit: 20000, OutputHardLimit: 10000, InputMicroPerMillion: 1, OutputMicroPerMillion: 1, AllOutputBounded: true, MaxActiveMS: 5000, NativeToolsQualified: true}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := accounting.NewAuthority(db, accounting.Config{RequireServiceJobs: true, BoundaryID: "inbox-test", ProjectID: "team", Limits: limits, Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	s.accountingRuntime.Authority = a
	s.pipeline.Canonical.Authority = a
	p := &aggregateQualifiedProvider{t: t, db: db, attempt: receipt.AttemptID}
	p.malformedFirst = repair
	build := func(authority orchestrator.BudgetAuthority) *orchestrator.Orchestrator {
		return orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("pinned", "pinned", p.Name()), map[string]orchestrator.LLMProvider{p.Name(): p}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(authority))
	}
	var paused *pausedContinuationAuthority
	if race {
		paused = &pausedContinuationAuthority{Authority: a, ready: make(chan struct{}), release: make(chan struct{})}
		s.pipeline.Orchestrator = build(paused)
	} else if crash {
		s.pipeline.Orchestrator = build(crashAfterPreparation{a})
	} else {
		s.pipeline.Orchestrator = build(a)
	}
	item, err := a.ClaimInbox(context.Background())
	if err != nil || item == nil {
		t.Fatalf("claim: %#v %v", item, err)
	}
	ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
	var liveDone chan error
	joined := false
	if race {
		liveDone = make(chan error, 1)
		go func() { liveDone <- s.runInboxItem(ctx, item.Request, item.RunID) }()
		defer func() {
			if !joined {
				close(paused.release)
				select {
				case <-liveDone:
				case <-time.After(5 * time.Second):
					t.Error("live frame did not stop")
				}
			}
		}()
		select {
		case <-paused.ready:
		case <-time.After(5 * time.Second):
			t.Fatal("live frame did not reach shared advancement")
		}
	} else {
		err = s.runInboxItem(ctx, item.Request, item.RunID)
	}
	if race {
		if p.calls.Load() != 1 {
			t.Fatal("live frame sent child before shared advancement")
		}
	} else if crash {
		if !errors.Is(err, errInboxPanic) || p.calls.Load() != 0 {
			t.Fatalf("missing preparation crash: calls=%d err=%v", p.calls.Load(), err)
		}
	} else if !errors.Is(err, orchestrator.ErrDurableContinuation) || p.calls.Load() != 1 {
		t.Fatalf("live formatter bypassed durable handoff: calls=%d err=%v", p.calls.Load(), err)
	}
	// Fresh service and empty memory store: only the stored action is executable.
	restarted := newInboxServer(t, dsn)
	restarted.accountingRuntime.Authority = a
	restarted.pipeline.Canonical.Authority = a
	restarted.pipeline.Orchestrator = build(a)
	restarted.pipeline.SCM = unavailableAcceptanceSCM{t}
	restarted.pipeline.TrustedPolicy = nil
	restarted.startInboxWorkers()
	want := 1
	if repair {
		want = 2
	}
	var indexed, reduced, terminal int
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		_ = db.QueryRow(`SELECT count(*) FROM budget_action_results WHERE status='recorded_unvalidated'`).Scan(&indexed)
		_ = db.QueryRow(`SELECT count(*) FROM budget_model_reductions WHERE state='validated'`).Scan(&reduced)
		_ = db.QueryRow(`SELECT count(*) FROM budget_jobs WHERE terminal_reduction_id IS NOT NULL`).Scan(&terminal)
		if indexed == want && reduced == 1 && terminal == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	restarted.inboxCancel()
	restarted.inboxWorkers.Wait()
	if race {
		close(paused.release)
		err = <-liveDone
		joined = true
		if !errors.Is(err, orchestrator.ErrDurableContinuation) {
			t.Fatalf("losing live frame did not hand off: %v", err)
		}
		if err = a.FinishInbox(context.Background(), item.RunID, item.ClaimToken, true); err == nil {
			t.Fatal("old live epoch finished continued work")
		}
	}
	if !crash {
		live, err := s.pipeline.Jobs.Get(context.Background(), item.RunID)
		if err != nil || live.Status != pipeline.StatusRunning || live.HILApproved || live.FinalReport != "" {
			t.Fatalf("handed-off frame falsely failed, completed or approved: %#v %v", live, err)
		}
	}
	if indexed != want || reduced != 1 || terminal != 1 || p.calls.Load() != int32(want) {
		t.Fatalf("automatic recovery/reduction missing: results=%d reductions=%d calls=%d", indexed, reduced, p.calls.Load())
	}
	if runs, err := restarted.pipeline.Jobs.List(context.Background()); err != nil || len(runs) != 0 {
		t.Fatalf("recovery replayed pipeline: %#v %v", runs, err)
	}
	var state, phase string
	var actions, holds int
	_ = db.QueryRow(`SELECT i.state,j.phase FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&state, &phase)
	_ = db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions)
	_ = db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
	if state != "completed" || phase != "finished" || actions != want || holds != 3*want {
		t.Fatalf("recovery invented continuation/settlement: state=%s phase=%s actions=%d holds=%d", state, phase, actions, holds)
	}
	status := httptest.NewRecorder()
	restarted.handleInbox(status, httptest.NewRequest(http.MethodGet, "/inbox?run_id="+receipt.RunID, nil))
	if status.Code != 200 || !strings.Contains(status.Body.String(), `"validated_reductions":1`) || !strings.Contains(status.Body.String(), `"pending_continuations":0`) || strings.Contains(status.Body.String(), "RawResponses") {
		t.Fatalf("reduction status hidden or response leaked: %d %s", status.Code, status.Body.String())
	}
	for _, viewer := range []*Server{restarted, s} {
		view := httptest.NewRecorder()
		viewer.handleRun(view, httptest.NewRequest(http.MethodGet, "/run?id="+receipt.RunID, nil))
		if view.Code != 200 || !strings.Contains(view.Body.String(), `"status":"drafted"`) || !strings.Contains(view.Body.String(), "7review Draft") || strings.Contains(view.Body.String(), `"hil_approved":true`) {
			t.Fatalf("restart lost or approved terminal run: %d %s", view.Code, view.Body.String())
		}
		listing := httptest.NewRecorder()
		viewer.handleRuns(listing, httptest.NewRequest(http.MethodGet, "/runs", nil))
		if listing.Code != 200 || strings.Count(listing.Body.String(), receipt.RunID) != 1 || !strings.Contains(listing.Body.String(), `"status":"drafted"`) {
			t.Fatalf("terminal listing missing or duplicated: %d %s", listing.Code, listing.Body.String())
		}
	}
	if repair {
		if _, err = db.Exec(`DELETE FROM budget_model_continuations WHERE child_action_id IN (SELECT x.action_id FROM budget_model_reduction_inputs x JOIN budget_jobs j ON j.terminal_reduction_id=x.reduction_id WHERE j.run_id=$1)`, receipt.RunID); err != nil {
			t.Fatal(err)
		}
		missing := httptest.NewRecorder()
		s.handleRun(missing, httptest.NewRequest(http.MethodGet, "/run?id="+receipt.RunID, nil))
		if missing.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing terminal proof fell back to memory: %d %s", missing.Code, missing.Body.String())
		}
	}
	if _, err = db.Exec(`UPDATE budget_model_reductions SET artifact_digest='corrupt-terminal' WHERE id=(SELECT terminal_reduction_id FROM budget_jobs WHERE run_id=$1)`, receipt.RunID); err != nil {
		t.Fatal(err)
	}
	view := httptest.NewRecorder()
	s.handleRun(view, httptest.NewRequest(http.MethodGet, "/run?id="+receipt.RunID, nil))
	if view.Code != http.StatusServiceUnavailable {
		t.Fatalf("corrupt durable result fell back to stale memory: %d %s", view.Code, view.Body.String())
	}
}

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
	var complete int
	if err := db.QueryRow(`SELECT count(*) FROM budget_inbox i JOIN budget_jobs j USING(run_id) JOIN budget_attempts a ON a.id=j.attempt_id JOIN budget_aggregates g USING(run_id) WHERE i.run_id=$1 AND i.state='pending' AND j.phase='admitted' AND i.claim_token IS NULL`, first.RunID).Scan(&complete); err != nil || complete != 1 {
		t.Fatalf("202 preceded complete aggregate commit: complete=%d err=%v", complete, err)
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

func TestCanonicalHTTPAggregateCommitFailureCannotAcknowledge(t *testing.T) {
	s, db := inboxServer(t)
	_, err := db.Exec(`CREATE FUNCTION reject_http_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected HTTP commit failure'; END $$; CREATE CONSTRAINT TRIGGER reject_http_acceptance AFTER INSERT ON budget_aggregates DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_http_acceptance()`)
	if err != nil {
		t.Fatal(err)
	}
	w := inboxWebhook(t, s, inboxBody, "commit-failure")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("failed commit HTTP=%d %s", w.Code, w.Body.String())
	}
	for _, table := range []string{"budget_inbox", "budget_jobs", "budget_attempts", "budget_heads", "budget_intakes", "budget_aggregates"} {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s survived failed acceptance: %d %v", table, count, err)
		}
	}
}

type unavailableAcceptanceSCM struct{ t *testing.T }

func (s unavailableAcceptanceSCM) Enrich(context.Context, review.Request) (*review.SCMContext, error) {
	s.t.Fatal("accepted delivery replay re-read SCM")
	return nil, nil
}

func TestCanonicalHTTPReplayResolvesCommittedReceiptBeforeCurrentPolicy(t *testing.T) {
	s, _ := inboxServer(t)
	first := inboxWebhook(t, s, inboxBody, "original-delivery")
	if first.Code != 202 {
		t.Fatalf("initial acceptance: %d %s", first.Code, first.Body.String())
	}
	var receipt reviewDispatchResult
	_ = json.Unmarshal(first.Body.Bytes(), &receipt)
	s.pipeline.SCM = unavailableAcceptanceSCM{t}
	s.pipeline.TrustedPolicy = nil
	s.cfg.WebhookReviewMode = "off"
	replay := inboxWebhook(t, s, inboxBody, "original-delivery")
	var got reviewDispatchResult
	_ = json.Unmarshal(replay.Body.Bytes(), &got)
	if replay.Code != 202 || !got.Durable || got.RunID != receipt.RunID {
		t.Fatalf("lost-response replay did not resolve original receipt: %d %s", replay.Code, replay.Body.String())
	}
}

type crashBefore202Writer struct{ header http.Header }

func (w crashBefore202Writer) Header() http.Header     { return w.header }
func (crashBefore202Writer) Write([]byte) (int, error) { os.Exit(44); return 0, nil }
func (crashBefore202Writer) WriteHeader(status int) {
	if status == http.StatusAccepted {
		os.Exit(43)
	}
	os.Exit(44)
}

func TestScenarioS15HTTPAcceptanceCrashChild(t *testing.T) {
	if os.Getenv("SEVENREVIEW_AGGREGATE_HTTP_CHILD") != "1" {
		t.Skip("subprocess helper")
	}
	s := newInboxServer(t, os.Getenv("SEVENREVIEW_AGGREGATE_HTTP_DSN"))
	s.cfg.WebhookJobTimeout = 60000
	r := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(inboxBody))
	r.Header.Set("X-GitHub-Event", "pull_request")
	r.Header.Set("X-GitHub-Delivery", "crash-before-202")
	r.Header.Set("X-Hub-Signature-256", githubSignature("secret", inboxBody))
	gitHubWebhookHandler("secret", s.handleWebhookReview)(crashBefore202Writer{http.Header{}}, r)
	t.Fatal("child did not exit at HTTP acknowledgement boundary")
}

// R14/S15: terminate after aggregate commit but before the caller receives 202.
func TestScenarioS15CommittedAggregateSurvivesLost202AndRestart(t *testing.T) {
	s, db, dsn := inboxServerDSN(t)
	crashAcceptedHTTP(t, dsn)
	var runID, attemptID string
	if err := db.QueryRow(`SELECT i.run_id,j.attempt_id FROM budget_inbox i JOIN budget_jobs j USING(run_id) JOIN budget_aggregates g USING(run_id) JOIN budget_attempts a ON a.id=j.attempt_id WHERE i.state='pending' AND i.claim_token IS NULL AND j.phase='admitted'`).Scan(&runID, &attemptID); err != nil {
		t.Fatalf("crash lost accepted aggregate: %v", err)
	}
	s.pipeline.SCM = unavailableAcceptanceSCM{t}
	s.pipeline.TrustedPolicy = nil
	replay := inboxWebhook(t, s, inboxBody, "crash-before-202")
	var receipt reviewDispatchResult
	_ = json.Unmarshal(replay.Body.Bytes(), &receipt)
	if replay.Code != 202 || receipt.RunID != runID || receipt.AttemptID != attemptID {
		t.Fatalf("lost 202 allocated a new aggregate: %d %s", replay.Code, replay.Body.String())
	}
	s.startInboxWorkers()
	var run *pipeline.Run
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		run, err = s.pipeline.Jobs.Get(context.Background(), runID)
		if err == nil && run.Status == pipeline.StatusFailed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run == nil || run.Source == nil || run.Source.Attempt.AttemptID != attemptID || !strings.Contains(run.Error, "qualified") {
		t.Fatalf("restart did not use frozen aggregate or dispatch failed open: %#v %v", run, err)
	}
	var attempts, actions int
	_ = db.QueryRow(`SELECT count(*) FROM budget_attempts`).Scan(&attempts)
	_ = db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions)
	if attempts != 1 || actions != 0 {
		t.Fatalf("restart reset identity or spent without qualification: attempts=%d actions=%d", attempts, actions)
	}
}

func crashAcceptedHTTP(t *testing.T, dsn string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestScenarioS15HTTPAcceptanceCrashChild$")
	cmd.Env = append(os.Environ(), "SEVENREVIEW_AGGREGATE_HTTP_CHILD=1", "SEVENREVIEW_AGGREGATE_HTTP_DSN="+dsn)
	err := cmd.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 43 {
		t.Fatalf("child did not reach committed before-202 boundary: %v", err)
	}
}

type aggregateQualifiedProvider struct {
	t              *testing.T
	db             *sql.DB
	attempt        string
	calls          atomic.Int32
	malformedFirst bool
}

func (*aggregateQualifiedProvider) Name() string { return "aggregate-fixture" }
func (p *aggregateQualifiedProvider) Complete(ctx context.Context, _ orchestrator.LLMRequest) (string, error) {
	call := p.calls.Add(1)
	var actions, holds int
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM budget_actions WHERE attempt_id=$1 AND state='sending'`, p.attempt).Scan(&actions); err != nil {
		return "", err
	}
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM budget_holds h JOIN budget_actions a ON a.id=h.action_id WHERE a.attempt_id=$1`, p.attempt).Scan(&holds); err != nil {
		return "", err
	}
	if actions != 1 || holds != 3*int(call) {
		p.t.Errorf("provider preceded atomic sending/holds: actions=%d holds=%d", actions, holds)
	}
	var fenced int
	if err := p.db.QueryRowContext(ctx, `SELECT count(*) FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.attempt_id=$1 AND a.state='sending' AND a.fenced AND l.sending_fence=l.fence AND l.fence>0 AND l.worker_id IS NOT NULL`, p.attempt).Scan(&fenced); err != nil {
		return "", err
	}
	if fenced != 1 {
		p.t.Errorf("provider preceded fenced sending commit: %d", fenced)
	}
	var raw []byte
	if err := p.db.QueryRowContext(ctx, `SELECT request FROM budget_actions WHERE attempt_id=$1 AND state='sending'`, p.attempt).Scan(&raw); err != nil {
		return "", err
	}
	var intent orchestrator.DispatchIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return "", err
	}
	cp, err := review.DecodeModelCheckpoint(intent.ModelCheckpoint)
	wantPhase := review.ModelReviewBatches
	if p.malformedFirst && call == 2 {
		wantPhase = review.ModelRepairFindings
	}
	if err != nil || cp.Source.Attempt.AttemptID != p.attempt || cp.Cursor.Phase != wantPhase || (wantPhase == review.ModelReviewBatches && cp.Cursor.BatchCount != 1) || cp.Cursor.BatchIndex != 0 || cp.Validation == nil || cp.Validation.MinConfidence != 0.45 || cp.Routing == nil || cp.Routing.Roles["formatter"].Routes[0].Model != "pinned" {
		p.t.Errorf("provider preceded immutable continuation checkpoint: %#v %v", cp.Cursor, err)
	}
	if p.malformedFirst && call == 1 {
		return "malformed parent response", nil
	}
	return "[]", nil
}

// R14/S15 positive counterpart uses an explicitly qualified in-memory provider;
// it does not qualify any production model or report financial settlement.
func TestScenarioS15AcceptedRecoveryDispatchesOnceUnderTestQualification(t *testing.T) {
	s, db, dsn := inboxServerDSN(t)
	crashAcceptedHTTP(t, dsn)
	s.pipeline.SCM = unavailableAcceptanceSCM{t}
	s.pipeline.TrustedPolicy = nil
	replay := inboxWebhook(t, s, inboxBody, "crash-before-202")
	var receipt reviewDispatchResult
	_ = json.Unmarshal(replay.Body.Bytes(), &receipt)
	if replay.Code != 202 || receipt.AttemptID == "" {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	limits := s.cfg.PolicyRuntimeCeilings.Clone()
	catalog, err := accounting.NewCatalog([]accounting.RouteBound{{Provider: "aggregate-fixture", Model: "pinned", Version: "test-v1", Currency: "USD", EvidenceRef: "test:bounded-in-memory-provider", ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), InputHardLimit: 20000, OutputHardLimit: 10000, InputMicroPerMillion: 1, OutputMicroPerMillion: 1, AllOutputBounded: true, MaxActiveMS: 5000, NativeToolsQualified: true}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := accounting.NewAuthority(db, accounting.Config{RequireServiceJobs: true, BoundaryID: "inbox-test", ProjectID: "team", Limits: limits, Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	s.accountingRuntime.Authority = authority
	s.pipeline.Canonical.Authority = authority
	provider := &aggregateQualifiedProvider{t: t, db: db, attempt: receipt.AttemptID}
	s.pipeline.Orchestrator = orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("pinned", "pinned", provider.Name()), map[string]orchestrator.LLMProvider{provider.Name(): provider}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(authority))
	s.startInboxWorkers()
	var state string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		_ = db.QueryRow(`SELECT state FROM budget_inbox WHERE run_id=$1`, receipt.RunID).Scan(&state)
		if state == "completed" || state == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, err := s.pipeline.Jobs.Get(context.Background(), receipt.RunID)
	if err != nil || state != "completed" || run.Status != pipeline.StatusDrafted || provider.calls.Load() != 1 {
		t.Fatalf("qualified recovery did not execute once: state=%s calls=%d run=%#v err=%v", state, provider.calls.Load(), run, err)
	}
	if run.HILApproved || run.FinalReport != "" {
		t.Fatal("accepted recovery fabricated human approval or publication")
	}
	s.inboxCancel()
	s.inboxWorkers.Wait()
	replayed := inboxWebhook(t, s, inboxBody, "crash-before-202")
	var after reviewDispatchResult
	_ = json.Unmarshal(replayed.Body.Bytes(), &after)
	if replayed.Code != 202 || after.RunID != receipt.RunID || after.Status != "completed" {
		t.Fatalf("terminal replay changed receipt: %d %s", replayed.Code, replayed.Body.String())
	}
	if next, err := authority.ClaimInbox(context.Background()); err != nil || next != nil {
		t.Fatalf("terminal replay made work executable: %#v %v", next, err)
	}
	var actions, holds, settled int
	_ = db.QueryRow(`SELECT count(*),count(*) FILTER (WHERE settled) FROM budget_actions`).Scan(&actions, &settled)
	_ = db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
	if actions != 1 || holds != 3 || settled != 0 || provider.calls.Load() != 1 {
		t.Fatalf("replay changed spend or inferred usage: actions=%d holds=%d settled=%d calls=%d", actions, holds, settled, provider.calls.Load())
	}
}

func TestCanonicalInboxWorkerRecoversPendingButNeverReplaysClaimedRun(t *testing.T) {
	s, db := inboxServer(t)
	req := review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", SourceSHA: "head", TargetSHA: "base", DeliveryID: "pending"}
	pending, err := s.enqueueReview(req, true)
	if err != nil {
		t.Fatal(err)
	}
	req.DeliveryID = "crashed"
	crashed, err := s.enqueueReview(req, true)
	if err != nil {
		t.Fatal(err)
	}
	// Mark the second row as claimed by a process that died after claiming.
	if _, err = db.Exec(`UPDATE budget_inbox SET state='running',claim_token='dead-worker' WHERE run_id=$1`, crashed.RunID); err != nil {
		t.Fatal(err)
	}
	s.pipeline.SCM = unavailableAcceptanceSCM{t}
	s.pipeline.TrustedPolicy = nil
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

type inboxBlockingReducer struct{ started chan time.Time }

func (s inboxBlockingReducer) Reduce(ctx context.Context, _ *review.Context) error {
	deadline, _ := ctx.Deadline()
	s.started <- deadline
	<-ctx.Done()
	return ctx.Err()
}
func (inboxBlockingReducer) Check(context.Context) error { return nil }

func TestCanonicalInboxShutdownPreservesClaimAndOriginalDeadline(t *testing.T) {
	s, db := inboxServer(t)
	started := make(chan time.Time, 1)
	s.pipeline.ContextReducer = inboxBlockingReducer{started}
	req := review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "shutdown"}
	queued, err := s.enqueueReview(req, true)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.accountingRuntime.Authority.LookupInbox(context.Background(), queued.RunID)
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

type inboxPanickingReducer struct{}

func (inboxPanickingReducer) Reduce(context.Context, *review.Context) error {
	panic("injected crash")
}
func (inboxPanickingReducer) Check(context.Context) error { return nil }

func TestCanonicalInboxPanicIsNotRecordedAsSafeFailure(t *testing.T) {
	s, db := inboxServer(t)
	s.pipeline.ContextReducer = inboxPanickingReducer{}
	item, err := s.enqueueReview(review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "panic"}, true)
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

func TestCanonicalReadinessChecksDurableInboxNotJustVolatileQueue(t *testing.T) {
	s, db := inboxServer(t)
	s.pipeline.Memory = pipeline.NoopMemoryStore{}
	s.pipeline.ContextReducer = pipeline.NoopContextReducer{}
	_, err := s.enqueueReview(review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "ready"}, true)
	if err != nil {
		t.Fatal(err)
	}
	health := s.readiness(context.Background())
	if !health.Ready || health.Inbox == nil || health.Inbox.Pending != 1 {
		t.Fatalf("durable backlog missing: %#v", health)
	}
	if _, err = db.Exec(`ALTER TABLE budget_inbox RENAME TO unavailable_inbox`); err != nil {
		t.Fatal(err)
	}
	health = s.readiness(context.Background())
	if health.Ready || health.Dependencies["durable_inbox"] == "" {
		t.Fatalf("volatile queue concealed durable storage outage: %#v", health)
	}
}

func TestInboxWorkerDoesNotDispatchPastUnavailableRecoveryJournal(t *testing.T) {
	s, db := inboxServer(t)
	queued, err := s.enqueueReview(review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "recovery-outage"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE budget_action_results RENAME TO unavailable_results`); err != nil {
		t.Fatal(err)
	}
	s.startInboxWorkers()
	time.Sleep(600 * time.Millisecond)
	var state string
	if err = db.QueryRow(`SELECT state FROM budget_inbox WHERE run_id=$1`, queued.RunID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("work passed unavailable recovery journal: %s %v", state, err)
	}
	if runs, err := s.pipeline.Jobs.List(context.Background()); err != nil || len(runs) != 0 {
		t.Fatalf("pipeline ran past recovery outage: %#v %v", runs, err)
	}
}

func TestInboxWorkersRecoverRecordedResultsWithoutRerunningPipeline(t *testing.T) {
	s, db := inboxServer(t)
	s.startInboxWorkers()
	queued, err := s.enqueueReview(review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", SourceSHA: "head", TargetSHA: "base", DeliveryID: "local-result"}, true)
	if err != nil {
		t.Fatal(err)
	}
	var run *pipeline.Run
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		run, err = s.pipeline.Jobs.Get(context.Background(), queued.RunID)
		if err == nil && run.Status == pipeline.StatusFailed && run.Source != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run == nil || run.Source == nil || run.Source.Attempt.AttemptID == "" {
		t.Fatalf("canonical run did not checkpoint: %#v %v", run, err)
	}
	s.inboxCancel()
	s.inboxWorkers.Wait()
	// Committed journal fixture for the service recovery adapter. Real sending,
	// holds and response-commit crash windows are tested in accounting.
	response := []byte("durable unvalidated response")
	outcome, _ := json.Marshal(orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: canonicalPayloadDigest(response), ElapsedMS: 1})
	intent, _ := json.Marshal(orchestrator.DispatchIntent{Job: &review.JobClaim{RunID: queued.RunID}})
	_, err = db.Exec(`INSERT INTO budget_actions(id,attempt_id,request_digest,intent_digest,request,quote,upper_amount,state,outcome) VALUES('recovery-fixture',$1,$2,$2,$3,$4,'{}','result_recorded',$5)`, run.Source.Attempt.AttemptID, canonicalPayloadDigest([]byte("request")), intent, []byte("{}"), outcome)
	if err != nil {
		t.Fatal(err)
	}
	s.pipeline.Jobs = pipeline.NewMemoryRunStore()
	s.startInboxWorkers()
	var count int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		_ = db.QueryRow(`SELECT count(*) FROM budget_action_results`).Scan(&count)
		if count == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if count != 1 {
		t.Fatal("worker did not recover committed local result")
	}
	if runs, err := s.pipeline.Jobs.List(context.Background()); err != nil || len(runs) != 0 {
		t.Fatalf("recovery reran the review pipeline: %#v %v", runs, err)
	}
	status, err := s.accountingRuntime.Authority.LookupInbox(context.Background(), queued.RunID)
	if err != nil || status.RecordedUnvalidated != 1 || status.State == "completed" {
		t.Fatalf("local result was promoted to successful review: %#v %v", status, err)
	}
}
