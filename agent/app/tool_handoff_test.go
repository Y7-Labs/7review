package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

type serviceToolProvider struct {
	t      *testing.T
	db     *sql.DB
	calls  atomic.Int32
	rounds int
	repair bool
}

func (*serviceToolProvider) Name() string { return "tool-service-fixture" }
func (p *serviceToolProvider) Complete(ctx context.Context, req orchestrator.LLMRequest) (string, error) {
	p.calls.Add(1)
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	var stored []byte
	var child, observations int
	err = p.db.QueryRowContext(ctx, `SELECT a.request,(SELECT count(*) FROM budget_model_tool_children c WHERE c.child_action_id=a.id),(SELECT count(*) FROM budget_model_tool_observations o WHERE o.attempt_id=a.attempt_id) FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.request_digest=$1 AND a.state='sending' AND a.fenced AND l.fence>0 AND l.sending_fence=l.fence`, digest).Scan(&stored, &child, &observations)
	if err != nil {
		return "", err
	}
	var intent orchestrator.DispatchIntent
	if err = json.Unmarshal(stored, &intent); err != nil {
		return "", err
	}
	if !bytes.Equal(intent.Payload, raw) || req.Model != "pinned" {
		p.t.Error("provider did not receive exact fenced request")
	}
	cp, err := review.DecodeModelCheckpoint(intent.ModelCheckpoint)
	if err != nil {
		return "", err
	}
	if cp.Cursor.Phase == review.ModelToolRound {
		if child != 1 || observations != cp.Cursor.Round || len(cp.Source.ToolRequests) != cp.Cursor.Round || len(cp.Source.ToolObservations) != cp.Cursor.Round || len(cp.RawResponses) != cp.Cursor.Round {
			p.t.Errorf("tool I/O preceded complete lineage/charges: child=%d obs=%d cursor=%#v", child, observations, cp.Cursor)
		}
		if p.repair {
			return "malformed tool followup", nil
		}
		if cp.Cursor.Round >= p.rounds {
			return "[]", nil
		}
	} else if cp.Cursor.Phase == review.ModelRepairFindings {
		return "[]", nil
	}
	return `{"tool_requests":[{"name":"get_changed_files","input":{}}],"findings":[]}`, nil
}

type serviceToolAuthority struct {
	*accounting.Authority
	mode           string
	ready, release chan struct{}
	once           sync.Once
}

type preparedOnlyToolBudget struct {
	orchestrator.PreparedBudgetAuthority
}

func (a *serviceToolAuthority) CommitModelToolObservations(ctx context.Context, id string, observer orchestrator.ModelToolObserver) (*review.ModelToolObservations, error) {
	if a.mode == "crash_before_observe" {
		panic("lost frame before observation commit")
	}
	if a.mode == "overlap_before_observe" {
		if err := a.pause(ctx); err != nil {
			return nil, err
		}
	}
	result, err := a.Authority.CommitModelToolObservations(ctx, id, observer)
	if err == nil && a.mode == "crash_after_observe" {
		panic("lost frame after observation commit")
	}
	if err == nil && a.mode == "overlap_after_observe" {
		if err := a.pause(ctx); err != nil {
			return nil, err
		}
	}
	return result, err
}
func (a *serviceToolAuthority) pause(ctx context.Context) error {
	a.once.Do(func() { close(a.ready) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.release:
		return nil
	}
}
func (a *serviceToolAuthority) AdvanceModelToolContinuation(ctx context.Context, id string, builder orchestrator.ModelToolContinuationBuilder) (orchestrator.PreparedAction, error) {
	p, err := a.Authority.AdvanceModelToolContinuation(ctx, id, builder)
	if err == nil && a.mode == "lost_child_ack" {
		return orchestrator.PreparedAction{}, errors.New("tool child commit acknowledgement lost")
	}
	return p, err
}

func qualifiedToolService(t *testing.T, s *Server, db *sql.DB) (*accounting.Authority, *serviceToolProvider, func(orchestrator.BudgetAuthority, string) *orchestrator.Orchestrator) {
	t.Helper()
	limits := s.cfg.PolicyRuntimeCeilings.Clone()
	catalog, err := accounting.NewCatalog([]accounting.RouteBound{{Provider: "tool-service-fixture", Model: "pinned", Version: "test-v1", Currency: "USD", EvidenceRef: "test:bounded-tool-provider", ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), InputHardLimit: 20000, OutputHardLimit: 10000, InputMicroPerMillion: 1, OutputMicroPerMillion: 1, AllOutputBounded: true, MaxActiveMS: 1000, NativeToolsQualified: true}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := accounting.NewAuthority(db, accounting.Config{RequireServiceJobs: true, BoundaryID: "inbox-test", ProjectID: "team", Limits: limits, Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	s.accountingRuntime.Authority = a
	s.pipeline.Canonical.Authority = a
	p := &serviceToolProvider{t: t, db: db, rounds: 1}
	build := func(authority orchestrator.BudgetAuthority, model string) *orchestrator.Orchestrator {
		cfg := orchestrator.DefaultOrchestratorConfig(model, model, p.Name())
		for name, role := range cfg.Roles {
			role.MaxTokens = 1024
			if model != "pinned" {
				role.MaxTokens = 2048
			}
			cfg.Roles[name] = role
		}
		return orchestrator.NewOrchestrator(cfg, map[string]orchestrator.LLMProvider{p.Name(): p}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(authority))
	}
	return a, p, build
}

func TestInboxAutomaticToolContinuationSurvivesRestartAndLiveOverlap(t *testing.T) {
	for _, mode := range []string{"live", "worker", "rounds", "repair", "crash_before_observe", "crash_after_observe", "overlap_before_observe", "overlap_after_observe", "observation_commit_failure", "child_commit_failure", "lost_child_ack", "missing_handoff_capability", "journal_failure"} {
		t.Run(mode, func(t *testing.T) {
			s, db, dsn := inboxServerDSN(t)
			s.cfg.WebhookJobTimeout = 20000
			accepted := inboxWebhook(t, s, inboxBody, "tool-handoff")
			if accepted.Code != http.StatusAccepted {
				t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
			}
			var receipt reviewDispatchResult
			if err := json.Unmarshal(accepted.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			a, p, build := qualifiedToolService(t, s, db)
			if mode == "rounds" {
				p.rounds = 3
			}
			p.repair = mode == "repair"
			wrapped := &serviceToolAuthority{Authority: a, mode: mode, ready: make(chan struct{}), release: make(chan struct{})}
			s.pipeline.Orchestrator = build(wrapped, "pinned")
			if mode == "missing_handoff_capability" {
				s.pipeline.Orchestrator = build(preparedOnlyToolBudget{a}, "pinned")
			}
			if mode == "journal_failure" {
				if _, err := db.Exec(`ALTER TABLE budget_action_results RENAME TO unavailable_tool_results`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "observation_commit_failure" || mode == "child_commit_failure" {
				table := "budget_model_tool_observations"
				if mode == "child_commit_failure" {
					table = "budget_model_tool_children"
				}
				if _, err := db.Exec(`CREATE FUNCTION fail_live_tool_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected live tool commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_live_tool_commit AFTER INSERT ON ` + table + ` DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_live_tool_commit()`); err != nil {
					t.Fatal(err)
				}
			}
			var item *accounting.InboxItem
			var liveDone chan error
			joined := false
			overlap := mode == "overlap_before_observe" || mode == "overlap_after_observe"
			if mode != "worker" {
				var err error
				item, err = a.ClaimInbox(context.Background())
				if err != nil || item == nil {
					t.Fatalf("claim: %#v %v", item, err)
				}
				ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
				if overlap {
					liveDone = make(chan error, 1)
					go func() { liveDone <- s.runInboxItem(ctx, item.Request, item.RunID) }()
					defer func() {
						if !joined {
							close(wrapped.release)
							select {
							case <-liveDone:
							case <-time.After(5 * time.Second):
								t.Error("paused tool frame did not exit")
							}
						}
					}()
					select {
					case <-wrapped.ready:
					case <-time.After(8 * time.Second):
						t.Fatal("live frame did not reach tool transition")
					}
				} else {
					err = s.runInboxItem(ctx, item.Request, item.RunID)
					crash := mode == "crash_before_observe" || mode == "crash_after_observe"
					if crash {
						if !errors.Is(err, errInboxPanic) {
							t.Fatalf("missing crash boundary: %v", err)
						}
					} else if !errors.Is(err, orchestrator.ErrDurableContinuation) {
						t.Fatalf("live frame used legacy tools or failed instead of handing off: %v", err)
					}
				}
				if p.calls.Load() != 1 {
					t.Fatalf("live frame dispatched a legacy followup: %d", p.calls.Load())
				}
				for _, success := range []bool{false, true} {
					if err := a.FinishInbox(context.Background(), item.RunID, item.ClaimToken, success); err == nil {
						t.Fatal("old frame could finish or fail recoverable work")
					}
				}
			}
			if mode == "observation_commit_failure" || mode == "child_commit_failure" {
				table := "budget_model_tool_observations"
				if mode == "child_commit_failure" {
					table = "budget_model_tool_children"
				}
				if _, err := db.Exec(`DROP TRIGGER fail_live_tool_commit ON ` + table); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "journal_failure" {
				if _, err := db.Exec(`ALTER TABLE unavailable_tool_results RENAME TO budget_action_results`); err != nil {
					t.Fatal(err)
				}
			}
			restarted := newInboxServer(t, dsn)
			restarted.accountingRuntime.Authority = a
			restarted.pipeline.Canonical.Authority = a
			restarted.pipeline.Orchestrator = build(a, "changed-current-route")
			if mode == "worker" {
				restarted.pipeline.Orchestrator = build(a, "pinned")
			} else {
				restarted.pipeline.SCM = unavailableAcceptanceSCM{t}
				restarted.pipeline.TrustedPolicy = nil
			}
			restarted.startInboxWorkers()
			terminal := 0
			for deadline := time.Now().Add(12 * time.Second); time.Now().Before(deadline); {
				if err := db.QueryRow(`SELECT count(*) FROM budget_jobs WHERE run_id=$1 AND terminal_reduction_id IS NOT NULL`, receipt.RunID).Scan(&terminal); err != nil {
					t.Fatal(err)
				}
				if terminal == 1 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			restarted.inboxCancel()
			restarted.inboxWorkers.Wait()
			if overlap {
				close(wrapped.release)
				joined = true
				if err := <-liveDone; !errors.Is(err, orchestrator.ErrDurableContinuation) {
					t.Fatalf("losing live frame failed handoff: %v", err)
				}
			}
			want := 1 + p.rounds
			if p.repair {
				want = 3
			}
			if terminal != 1 || p.calls.Load() != int32(want) {
				t.Fatalf("automatic tool progress missing or duplicated: terminal=%d calls=%d want=%d", terminal, p.calls.Load(), want)
			}
			var actions, holds, observations, children, settled int
			if err := db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_model_tool_children),(SELECT count(*) FROM budget_actions WHERE settled)`).Scan(&actions, &holds, &observations, &children, &settled); err != nil || actions != want || holds != 3*want || observations != p.rounds || children != p.rounds || settled != 0 {
				t.Fatalf("automatic continuation lost accounting: %d %d %d %d %d %v", actions, holds, observations, children, settled, err)
			}
			if mode != "worker" {
				if runs, err := restarted.pipeline.Jobs.List(context.Background()); err != nil || len(runs) != 0 {
					t.Fatal("restart replayed the pipeline")
				}
			}
			view := httptest.NewRecorder()
			inbox := httptest.NewRecorder()
			restarted.handleInbox(inbox, httptest.NewRequest(http.MethodGet, "/inbox?run_id="+receipt.RunID, nil))
			if inbox.Code != 200 || !bytes.Contains(inbox.Body.Bytes(), []byte(`"pending_continuations":0`)) {
				t.Fatalf("completed tool parents still counted as pending: %d %s", inbox.Code, inbox.Body.String())
			}
			restarted.handleRun(view, httptest.NewRequest(http.MethodGet, "/run?id="+receipt.RunID, nil))
			var dto runDTO
			if err := json.Unmarshal(view.Body.Bytes(), &dto); err != nil || view.Code != 200 || dto.Status != pipeline.StatusDrafted || dto.HILApproved || dto.FinalReport != "" {
				t.Fatalf("tool continuation lost or published draft: %d %s %v", view.Code, view.Body.String(), err)
			}
			if mode != "worker" {
				memory, err := s.pipeline.Jobs.Get(context.Background(), receipt.RunID)
				if err != nil || memory.Status != pipeline.StatusRunning {
					t.Fatalf("abandoned frame falsely failed or completed: %#v %v", memory, err)
				}
			}
		})
	}
}

type limitedToolAttemptPolicy struct{ limits review.ResourceLimits }

func (p limitedToolAttemptPolicy) Evaluate(ctx context.Context, r review.Request, scm *review.SCMContext) (pipeline.TrustedPolicyResult, error) {
	result, err := (canonicalServicePolicy{p.limits}).Evaluate(ctx, r, scm)
	if err == nil && r.ChangeID == "7" {
		result.Effective.Limits.Attempt.ModelCalls = 1
	}
	return result, err
}

type limitedPreparedProjectPolicy struct{ limits review.ResourceLimits }

func (p limitedPreparedProjectPolicy) Evaluate(ctx context.Context, r review.Request, scm *review.SCMContext) (pipeline.TrustedPolicyResult, error) {
	result, err := (canonicalServicePolicy{p.limits}).Evaluate(ctx, r, scm)
	if err == nil && r.ChangeID == "7" {
		result.Effective.Limits.Project.ModelCalls = 1
	}
	return result, err
}

func TestInboxPreparedBudgetWaitDoesNotStopHealthyContinuation(t *testing.T) {
	s, db := inboxServer(t)
	s.cfg.WebhookJobTimeout = 20000
	a, p, build := qualifiedToolService(t, s, db)
	s.pipeline.TrustedPolicy = limitedPreparedProjectPolicy{s.cfg.PolicyRuntimeCeilings.Clone()}
	s.pipeline.Orchestrator = build(crashAfterPreparation{a}, "pinned")
	for _, change := range []string{"7", "8"} {
		body := strings.Replace(inboxBody, `"number":7`, `"number":`+change, 1)
		if w := inboxWebhook(t, s, body, "prepared-pressure-"+change); w.Code != 202 {
			t.Fatalf("accept: %d %s", w.Code, w.Body.String())
		}
		item, err := a.ClaimInbox(context.Background())
		if err != nil || item == nil {
			t.Fatalf("claim: %#v %v", item, err)
		}
		ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
		if err := s.runInboxItem(ctx, item.Request, item.RunID); !errors.Is(err, errInboxPanic) {
			t.Fatalf("missing prepared crash: %v", err)
		}
	}
	if p.calls.Load() != 0 {
		t.Fatal("prepared frames performed provider I/O")
	}
	s.pipeline.Orchestrator = build(a, "changed-current-route")
	s.pipeline.SCM = unavailableAcceptanceSCM{t}
	s.pipeline.TrustedPolicy = nil
	s.startInboxWorkers()
	completed := 0
	for until := time.Now().Add(10 * time.Second); time.Now().Before(until); {
		if err := db.QueryRow(`SELECT count(*) FROM budget_jobs WHERE terminal_reduction_id IS NOT NULL`).Scan(&completed); err != nil {
			t.Fatal(err)
		}
		if completed == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.inboxCancel()
	s.inboxWorkers.Wait()
	if completed != 1 || p.calls.Load() != 2 {
		t.Fatalf("prepared wait blocked healthy reduction/continuation or overspent: completed=%d calls=%d", completed, p.calls.Load())
	}
	var prepared, holds int
	if err := db.QueryRow(`SELECT count(*),(SELECT count(*) FROM budget_holds h JOIN budget_actions x ON x.id=h.action_id WHERE x.state='prepared') FROM budget_actions WHERE state='prepared'`).Scan(&prepared, &holds); err != nil || prepared != 1 || holds != 3 {
		t.Fatalf("prepared wait lost hold or resent blocked action: %d %d %v", prepared, holds, err)
	}
}

func TestInboxBudgetWaitingToolPlanDoesNotStarveNewHealthyInbox(t *testing.T) {
	s, db := inboxServer(t)
	s.cfg.WebhookJobTimeout = 20000
	a, p, build := qualifiedToolService(t, s, db)
	s.pipeline.Orchestrator = build(a, "pinned")
	s.pipeline.TrustedPolicy = limitedToolAttemptPolicy{s.cfg.PolicyRuntimeCeilings.Clone()}
	first := inboxWebhook(t, s, inboxBody, "budget-waiting-tools")
	var blocked reviewDispatchResult
	if err := json.Unmarshal(first.Body.Bytes(), &blocked); err != nil || first.Code != 202 {
		t.Fatalf("accept blocked: %d %s %v", first.Code, first.Body.String(), err)
	}
	before, err := a.LookupInbox(context.Background(), blocked.RunID)
	if err != nil {
		t.Fatal(err)
	}
	s.startInboxWorkers()
	observations := 0
	for until := time.Now().Add(8 * time.Second); time.Now().Before(until); {
		if err := db.QueryRow(`SELECT count(*) FROM budget_model_tool_observations WHERE run_id=$1`, blocked.RunID).Scan(&observations); err != nil {
			t.Fatal(err)
		}
		if observations == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if observations != 1 {
		t.Fatal("first plan did not reach model-budget wait")
	}
	body := strings.Replace(inboxBody, `"number":7`, `"number":8`, 1)
	second := inboxWebhook(t, s, body, "healthy-after-budget-wait")
	var healthy reviewDispatchResult
	if err := json.Unmarshal(second.Body.Bytes(), &healthy); err != nil || second.Code != 202 {
		t.Fatalf("accept healthy: %d %s %v", second.Code, second.Body.String(), err)
	}
	terminal := 0
	for until := time.Now().Add(10 * time.Second); time.Now().Before(until); {
		if err := db.QueryRow(`SELECT count(*) FROM budget_jobs WHERE run_id=$1 AND terminal_reduction_id IS NOT NULL`, healthy.RunID).Scan(&terminal); err != nil {
			t.Fatal(err)
		}
		if terminal == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.inboxCancel()
	s.inboxWorkers.Wait()
	if terminal != 1 || p.calls.Load() != 3 {
		t.Fatalf("budget wait monopolized workers or overspent: terminal=%d calls=%d", terminal, p.calls.Load())
	}
	after, err := a.LookupInbox(context.Background(), blocked.RunID)
	if err != nil || after.State != "running" || after.PendingContinuations != 1 || !after.Deadline.Equal(before.Deadline) {
		t.Fatalf("budget wait finished, refunded or extended original work: %#v %v", after, err)
	}
	var actions, holds int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions WHERE attempt_id=$1),(SELECT count(*) FROM budget_holds h JOIN budget_actions a ON a.id=h.action_id WHERE a.attempt_id=$1)`, blocked.AttemptID).Scan(&actions, &holds); err != nil || actions != 1 || holds != 3 {
		t.Fatalf("budget wait changed held usage: %d %d %v", actions, holds, err)
	}
}
