package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

type batchServiceSCM struct{}

func (batchServiceSCM) Enrich(ctx context.Context, r review.Request) (*review.SCMContext, error) {
	s, err := (canonicalServiceSCM{}).Enrich(ctx, r)
	if err == nil {
		s.Files = append(s.Files, review.ChangedFile{NewPath: "controller.go", Patch: "+second change", Status: "modified"})
	}
	return s, err
}

type batchHandoffAuthority struct {
	*accounting.Authority
	ready, release chan struct{}
	crash          bool
	badReceipt     bool
	lostAck        bool
}

func (a *batchHandoffAuthority) PrepareModelBatch(ctx context.Context, intents []orchestrator.DispatchIntent) ([]orchestrator.PreparedAction, error) {
	prepared, err := a.Authority.PrepareModelBatch(ctx, intents)
	if err != nil {
		return nil, err
	}
	if a.crash {
		panic("process lost after atomic batch preparation")
	}
	if a.lostAck {
		return nil, errors.New("atomic prepare acknowledgement lost after commit")
	}
	if a.badReceipt {
		prepared[1].RequestDigest = "invalid-receipt"
	}
	if a.ready != nil {
		close(a.ready)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-a.release:
		}
	}
	return prepared, nil
}

type batchHandoffProvider struct {
	t       *testing.T
	db      *sql.DB
	attempt string
	repair  bool
	tools   bool
	calls   atomic.Int32
}

func (*batchHandoffProvider) Name() string { return "batch-fixture" }
func (p *batchHandoffProvider) Complete(ctx context.Context, req orchestrator.LLMRequest) (string, error) {
	call := p.calls.Add(1)
	var sending, seals, holds int
	var raw []byte
	if err := p.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM budget_actions WHERE attempt_id=$1 AND state='sending'),(SELECT count(*) FROM budget_model_batches WHERE attempt_id=$1),(SELECT count(*) FROM budget_holds h JOIN budget_actions a ON a.id=h.action_id WHERE a.attempt_id=$1)`, p.attempt).Scan(&sending, &seals, &holds); err != nil {
		return "", err
	}
	wantHolds := 6
	if call == 3 {
		wantHolds = 9
	}
	if sending != 1 || seals != 1 || holds != wantHolds {
		p.t.Errorf("batch I/O preceded complete preparation: sending=%d seals=%d holds=%d", sending, seals, holds)
	}
	if err := p.db.QueryRowContext(ctx, `SELECT a.request FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.attempt_id=$1 AND a.state='sending' AND a.fenced AND l.sending_fence=l.fence AND l.fence>0 AND l.worker_id IS NOT NULL`, p.attempt).Scan(&raw); err != nil {
		return "", err
	}
	var intent orchestrator.DispatchIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return "", err
	}
	request, _ := json.Marshal(req)
	if !bytes.Equal(request, intent.Payload) || req.Model != "pinned" || req.MaxTokens != 1024 {
		p.t.Error("recovery changed the frozen batch request")
	}
	cp, err := review.DecodeModelCheckpoint(intent.ModelCheckpoint)
	if err != nil || cp.Source.Attempt.AttemptID != p.attempt || cp.Source.Diff == nil || len(cp.Source.Diff.Files) != 2 {
		p.t.Errorf("batch lost canonical source: %v", err)
	}
	if call <= 2 && (cp.Cursor.Phase != review.ModelReviewBatches || cp.Cursor.BatchCount != 2 || cp.Cursor.BatchIndex != int(call)-1) {
		p.t.Errorf("batch cursor changed: %#v", cp.Cursor)
	}
	if p.repair && call == 1 {
		return "malformed batch response", nil
	}
	if p.tools && call == 1 {
		return `{"tool_requests":[{"name":"get_changed_files","input":{},"reason":"more context"}],"findings":[]}`, nil
	}
	return "[]", nil
}

type singleDispatchOnly struct{ a *accounting.Authority }

func (s singleDispatchOnly) ReserveAndBegin(ctx context.Context, i orchestrator.DispatchIntent) (orchestrator.DispatchPermit, error) {
	return s.a.ReserveAndBegin(ctx, i)
}
func (s singleDispatchOnly) RecordOutcome(ctx context.Context, p orchestrator.DispatchPermit, o orchestrator.DispatchOutcome) error {
	return s.a.RecordOutcome(ctx, p, o)
}

func TestCanonicalLiveBatchHandoffSurvivesRestartAndRecoveryOverlap(t *testing.T) {
	for _, mode := range []string{"normal", "worker", "repair", "crash", "overlap", "tools_pending", "commit_failure", "missing_authority", "bad_receipt", "lost_ack"} {
		t.Run(mode, func(t *testing.T) {
			s, db, dsn := inboxServerDSN(t)
			s.cfg.MaxDiffTokens = 1
			s.cfg.WebhookJobTimeout = 15000
			s.pipeline.SCM = batchServiceSCM{}
			accepted := inboxWebhook(t, s, inboxBody, "batch-handoff")
			if accepted.Code != 202 {
				t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
			}
			var receipt reviewDispatchResult
			if err := json.Unmarshal(accepted.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			limits := s.cfg.PolicyRuntimeCeilings.Clone()
			catalog, err := accounting.NewCatalog([]accounting.RouteBound{{Provider: "batch-fixture", Model: "pinned", Version: "test-v1", Currency: "USD", EvidenceRef: "test:bounded-batch-provider", ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), InputHardLimit: 20000, OutputHardLimit: 10000, InputMicroPerMillion: 1, OutputMicroPerMillion: 1, AllOutputBounded: true, MaxActiveMS: 3000, NativeToolsQualified: true}})
			if err != nil {
				t.Fatal(err)
			}
			a, err := accounting.NewAuthority(db, accounting.Config{RequireServiceJobs: true, BoundaryID: "inbox-test", ProjectID: "team", Limits: limits, Catalog: catalog})
			if err != nil {
				t.Fatal(err)
			}
			provider := &batchHandoffProvider{t: t, db: db, attempt: receipt.AttemptID, repair: mode == "repair", tools: mode == "tools_pending"}
			build := func(authority orchestrator.BudgetAuthority, model string) *orchestrator.Orchestrator {
				cfg := orchestrator.DefaultOrchestratorConfig(model, model, provider.Name())
				for name, role := range cfg.Roles {
					role.MaxTokens = 1024
					if model != "pinned" {
						role.MaxTokens = 2048
					}
					cfg.Roles[name] = role
				}
				return orchestrator.NewOrchestrator(cfg, map[string]orchestrator.LLMProvider{provider.Name(): provider}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(authority))
			}
			s.accountingRuntime.Authority = a
			s.pipeline.Canonical.Authority = a
			wrapped := &batchHandoffAuthority{Authority: a, crash: mode == "crash", badReceipt: mode == "bad_receipt", lostAck: mode == "lost_ack"}
			if mode == "overlap" {
				wrapped.ready = make(chan struct{})
				wrapped.release = make(chan struct{})
			}
			s.pipeline.Orchestrator = build(wrapped, "pinned")
			if mode == "missing_authority" {
				s.pipeline.Orchestrator = build(singleDispatchOnly{a}, "pinned")
			}
			if mode == "commit_failure" {
				if _, err = db.Exec(`CREATE FUNCTION fail_live_batch_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected batch commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_live_batch_commit AFTER INSERT ON budget_model_batches DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_live_batch_commit()`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "worker" {
				s.startInboxWorkers()
				var projection *review.ModelJobProjection
				for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
					projection, err = a.LookupModelJob(context.Background(), receipt.RunID)
					if err == nil {
						break
					}
					if !errors.Is(err, accounting.ErrModelJobNotFound) {
						t.Fatal(err)
					}
					time.Sleep(10 * time.Millisecond)
				}
				if projection == nil || !projection.Succeeded || provider.calls.Load() != 2 {
					t.Fatalf("HTTP-to-worker batch journey failed: %#v calls=%d %v", projection, provider.calls.Load(), err)
				}
				live, err := s.pipeline.Jobs.Get(context.Background(), receipt.RunID)
				if err != nil || live.Status != pipeline.StatusRunning || live.HILApproved {
					t.Fatalf("worker completed or approved abandoned live frame: %#v %v", live, err)
				}
				return
			}
			item, err := a.ClaimInbox(context.Background())
			if err != nil || item == nil {
				t.Fatalf("claim: %#v %v", item, err)
			}
			ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
			var liveDone chan error
			joined := false
			if mode == "overlap" {
				liveDone = make(chan error, 1)
				go func() { liveDone <- s.runInboxItem(ctx, item.Request, item.RunID) }()
				defer func() {
					if !joined {
						close(wrapped.release)
						select {
						case <-liveDone:
						case <-time.After(5 * time.Second):
							t.Error("live batch frame did not stop")
						}
					}
				}()
				select {
				case <-wrapped.ready:
				case <-time.After(5 * time.Second):
					t.Fatal("live frame never committed atomic batch")
				}
			} else {
				err = s.runInboxItem(ctx, item.Request, item.RunID)
				if mode == "commit_failure" || mode == "missing_authority" || mode == "bad_receipt" || mode == "lost_ack" {
					if !orchestrator.IsBudgetError(err) || provider.calls.Load() != 0 {
						t.Fatalf("failed batch fell back to live calls: %d %v", provider.calls.Load(), err)
					}
					var actions, holds, seals, sending int
					if err = db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_model_batches),(SELECT count(*) FROM budget_actions WHERE state<>'prepared')`).Scan(&actions, &holds, &seals, &sending); err != nil {
						t.Fatal(err)
					}
					want := 0
					if mode == "bad_receipt" || mode == "lost_ack" {
						want = 2
					}
					if actions != want || holds != 3*want || seals != want/2 || sending != 0 {
						t.Fatalf("failed batch leaked partial effects: %d %d %d %d", actions, holds, seals, sending)
					}
					if want == 0 {
						return
					}
					if finishErr := a.FinishInbox(ctx, item.RunID, item.ClaimToken, false); finishErr == nil {
						t.Fatal("unconfirmed acknowledgement stranded committed sealed batch")
					}
				}
				if mode == "crash" {
					if !errors.Is(err, errInboxPanic) {
						t.Fatalf("missing batch crash: %v", err)
					}
				} else if mode != "bad_receipt" && mode != "lost_ack" && !errors.Is(err, orchestrator.ErrDurableContinuation) {
					t.Fatalf("batch stayed in legacy live dispatch: %v", err)
				}
			}
			if provider.calls.Load() != 0 {
				t.Fatal("live frame sent before durable handoff")
			}
			restarted := newInboxServer(t, dsn)
			restarted.accountingRuntime.Authority = a
			restarted.pipeline.Canonical.Authority = a
			restarted.pipeline.Orchestrator = build(a, "changed-after-preparation")
			restarted.pipeline.SCM = unavailableAcceptanceSCM{t}
			restarted.pipeline.TrustedPolicy = nil
			restarted.startInboxWorkers()
			terminal := 0
			for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
				query := `SELECT count(*) FROM budget_jobs WHERE run_id=$1 AND terminal_reduction_id IS NOT NULL`
				if mode == "tools_pending" {
					query = `SELECT count(*) FROM budget_model_reductions WHERE run_id=$1 AND state='needs_tools'`
				}
				if err = db.QueryRow(query, receipt.RunID).Scan(&terminal); err != nil {
					t.Fatal(err)
				}
				if terminal == 1 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if terminal != 1 {
				t.Fatal("restart stranded sealed batch")
			}
			if mode == "overlap" {
				close(wrapped.release)
				joined = true
				select {
				case err = <-liveDone:
				case <-time.After(5 * time.Second):
					t.Fatal("old live batch frame did not hand off")
				}
				if !errors.Is(err, orchestrator.ErrDurableContinuation) {
					t.Fatalf("old frame failed durable handoff: %v", err)
				}
			}
			want := int32(2)
			if mode == "repair" {
				want = 3
			}
			if provider.calls.Load() != want {
				t.Fatalf("batch replay or missing effect: calls=%d want=%d", provider.calls.Load(), want)
			}
			if mode == "tools_pending" {
				var actionID string
				if err = db.QueryRow(`SELECT id FROM budget_actions ORDER BY id LIMIT 1`).Scan(&actionID); err != nil {
					t.Fatal(err)
				}
				artifact, err := a.LookupModelReduction(context.Background(), actionID)
				if err != nil || artifact.State != review.ModelReductionNeedsTools || len(artifact.ToolRequests) != 1 || len(artifact.Source.ToolObservations) != 0 || artifact.Source.Report.Draft != "" || artifact.Source.Report.Final != "" {
					t.Fatalf("tool plan invented observations or completion: %#v %v", artifact, err)
				}
				if p, err := a.LookupModelJob(context.Background(), receipt.RunID); !errors.Is(err, accounting.ErrModelJobNotFound) || p != nil {
					t.Fatalf("unfinished tool plan became terminal: %#v %v", p, err)
				}
				return
			}
			if err = a.FinishInbox(ctx, item.RunID, item.ClaimToken, true); err == nil {
				t.Fatal("old live frame retained completion authority")
			}
			view := httptest.NewRecorder()
			s.handleRun(view, httptest.NewRequest(http.MethodGet, "/run?id="+receipt.RunID, nil))
			var dto runDTO
			if err = json.Unmarshal(view.Body.Bytes(), &dto); view.Code != 200 || err != nil || dto.Status != pipeline.StatusDrafted || dto.HILApproved || dto.FinalReport != "" {
				t.Fatalf("batch lost or published terminal draft: %d %s", view.Code, view.Body.String())
			}
			if runs, err := restarted.pipeline.Jobs.List(context.Background()); err != nil || len(runs) != 0 {
				t.Fatal("restart replayed the full pipeline")
			}
		})
	}
}
