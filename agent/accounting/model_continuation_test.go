package accounting

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func formatterParentFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent, *review.JobClaim) {
	a, i, claim, _ := formatterParentFixtureDSN(t)
	return a, i, claim
}

func formatterParentFixtureDSN(t *testing.T) (*Authority, orchestrator.DispatchIntent, *review.JobClaim, string) {
	t.Helper()
	a, i, dsn := preparedActionFixture(t)
	ctx := context.Background()
	agg, err := a.LoadAggregate(ctx, *i.Job)
	if err != nil {
		t.Fatal(err)
	}
	agg.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "service.go", Patch: "+change"}}}
	routing := &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{"reasoner": {Routes: []review.ModelRoute{{Provider: i.Provider, Model: i.Model}}, MaxTokens: 20}, "formatter": {Routes: []review.ModelRoute{{Provider: i.Provider, Model: i.Model}}, MaxTokens: 20}}}
	cpctx, err := review.WithModelCheckpoint(review.WithModelRouting(review.WithModelValidation(ctx, &review.ModelValidation{Version: 1, MinConfidence: 0.45}), routing), &review.Context{Source: agg.Source}, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	i.Invocation = "complete"
	i.Step = "step5"
	i.ModelCheckpoint = review.ModelCheckpointFromContext(cpctx)
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.ReclaimPrepared(ctx, p, "parent-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("malformed parent response")
	if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: raw, ResponseReceived: true, ResponseDigest: hash(raw), ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RecoverActionResults(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil || n != 1 {
		t.Fatalf("parent reduction: %d %v", n, err)
	}
	return a, i, c.Job, dsn
}

func TestPostgresFormatterChildAdvancementIsAtomicAndConfined(t *testing.T) {
	a, parent, old := formatterParentFixture(t)
	ctx := context.Background()
	_, err := a.db.Exec(`CREATE FUNCTION fail_model_child() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected child commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_model_child AFTER INSERT ON budget_model_continuations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_model_child()`)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := a.AdvanceModelContinuation(ctx, parent.ActionID, pipeline.ModelContinuationV1{}); err == nil || p.ID != "" {
		t.Fatalf("uncommitted child escaped: %#v %v", p, err)
	}
	var token, restriction string
	var count int
	if err = a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&token, &restriction); err != nil {
		t.Fatal(err)
	}
	if token != old.Token || restriction != parent.ActionID {
		t.Fatal("failed child changed parent authority")
	}
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("child reservation escaped rollback: %d %v", count, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_model_child ON budget_model_continuations`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	children := make(chan orchestrator.PreparedAction, 2)
	errs := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := a.AdvanceModelContinuation(ctx, parent.ActionID, pipeline.ModelContinuationV1{})
			children <- p
			errs <- e
		}()
	}
	wg.Wait()
	close(children)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for child := range children {
		if child.ID == "" || (id != "" && id != child.ID) {
			t.Fatalf("duplicate child identity: %#v", child)
		}
		id = child.ID
	}
	if err = a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&token, &restriction); err != nil {
		t.Fatal(err)
	}
	if token == old.Token || restriction != id {
		t.Fatal("child did not fence and confine job ownership")
	}
	var holds, links int
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_model_continuations`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if count != 2 || holds != 6 || links != 1 {
		t.Fatalf("child reserved twice: actions=%d holds=%d links=%d", count, holds, links)
	}
	tx, err := a.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := a.storedAction(ctx, tx, id)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	var req orchestrator.LLMRequest
	if err = json.Unmarshal(child.Payload, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != parent.Model || child.Role != "formatter" || child.Step != "repair_findings" || child.Job.Token != token || len(req.Tools) != 0 {
		t.Fatalf("child template changed: %#v", child)
	}
	other := child
	other.ActionID = "unrelated-child"
	if _, err = a.PrepareAction(ctx, other); err == nil {
		t.Fatal("child token authorized unrelated spending")
	}
	other.Job = old
	if _, err = a.PrepareAction(ctx, other); err == nil {
		t.Fatal("old parent token retained spending authority")
	}
}

type continuedFormatterProvider struct {
	t      *testing.T
	a      *Authority
	intent orchestrator.DispatchIntent
	calls  atomic.Int32
}

func (p *continuedFormatterProvider) Name() string { return p.intent.Provider }
func (p *continuedFormatterProvider) Complete(ctx context.Context, req orchestrator.LLMRequest) (string, error) {
	p.calls.Add(1)
	var state string
	if err := p.a.db.QueryRowContext(ctx, `SELECT state FROM budget_actions WHERE id=$1`, p.intent.ActionID).Scan(&state); err != nil {
		return "", err
	}
	if state != "sending" || req.Model != p.intent.Model || len(req.Tools) != 0 {
		p.t.Error("continued provider preceded sending or changed request")
	}
	return "[]", nil
}

func TestPostgresContinuedFormatterProcessDeathNeverReplaysParentOrSendingChild(t *testing.T) {
	for _, stage := range []string{"prepare", "claim", "send"} {
		t.Run(stage, func(t *testing.T) {
			a, parent, _, dsn := formatterParentFixtureDSN(t)
			ctx := context.Background()
			p, err := a.AdvanceModelContinuation(ctx, parent.ActionID, pipeline.ModelContinuationV1{})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := a.begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			i, _, err := a.storedAction(ctx, tx, p.ID)
			tx.Rollback()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(i)
			child := exec.Command(os.Args[0], "-test.run=^TestPreparedActionCrashChild$")
			child.Env = append(os.Environ(), "SEVENREVIEW_PREPARED_CRASH_STAGE="+stage, "SEVENREVIEW_PREPARED_CRASH_DSN="+dsn, "SEVENREVIEW_PREPARED_CRASH_INTENT="+string(raw))
			err = child.Run()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 67 {
				t.Fatalf("child boundary: %v", err)
			}
			if stage == "claim" {
				if _, err = a.db.Exec(`SELECT pg_sleep(greatest(0,extract(epoch FROM (lease_until-clock_timestamp())))+0.002) FROM budget_action_leases WHERE action_id=$1`, p.ID); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "send" {
				if _, err = a.db.Exec(`UPDATE budget_action_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE action_id=$1`, p.ID); err != nil {
					t.Fatal(err)
				}
			}
			provider := &continuedFormatterProvider{t: t, a: a, intent: i}
			limits := ledgerLimits()
			o := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("different-current-model", "different-current-formatter", provider.Name()), map[string]orchestrator.LLMProvider{provider.Name(): provider}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(a))
			n, err := o.RecoverPreparedActions(ctx, 8)
			want := 1
			if stage == "send" {
				want = 0
			}
			if err != nil || n != want || provider.calls.Load() != int32(want) {
				t.Fatalf("continued recovery: n=%d calls=%d err=%v", n, provider.calls.Load(), err)
			}
			if n, err = o.RecoverPreparedActions(ctx, 8); err != nil || n != 0 || provider.calls.Load() != int32(want) {
				t.Fatalf("continued resend: %d %v", n, err)
			}
			if want == 1 {
				if _, err = a.RecoverActionResults(ctx, 8); err != nil {
					t.Fatal(err)
				}
				if n, err = a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil || n != 1 {
					t.Fatalf("continued reduction: %d %v", n, err)
				}
				artifact, err := a.LookupModelReduction(ctx, p.ID)
				if err != nil || artifact.State != review.ModelReductionValidated || artifact.Source.Report.Final != "" {
					t.Fatalf("continued result: %#v %v", artifact, err)
				}
			}
			replay, err := a.AdvanceModelContinuation(ctx, parent.ActionID, pipeline.ModelContinuationV1{})
			if err != nil || replay.ID != p.ID || !replay.AlreadyStarted {
				t.Fatalf("parent replay created another effect: %#v %v", replay, err)
			}
			var actions, holds int
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions); err != nil {
				t.Fatal(err)
			}
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil {
				t.Fatal(err)
			}
			if actions != 2 || holds != 6 {
				t.Fatalf("continued recovery reset accounting: %d %d", actions, holds)
			}
		})
	}
}

func TestPostgresChildBeginRechecksParentProofAfterClaim(t *testing.T) {
	a, parent, _ := formatterParentFixture(t)
	ctx := context.Background()
	p, err := a.AdvanceModelContinuation(ctx, parent.ActionID, pipeline.ModelContinuationV1{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.ReclaimPrepared(ctx, p, "claimed-child", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_model_reductions SET artifact_digest='changed'`); err != nil {
		t.Fatal(err)
	}
	if permit, err := a.BeginPrepared(ctx, c); err == nil || permit.ID != "" {
		t.Fatalf("changed parent allowed child I/O: %#v %v", permit, err)
	}
	var state string
	if err = a.db.QueryRow(`SELECT state FROM budget_actions WHERE id=$1`, p.ID).Scan(&state); err != nil || state != "prepared" {
		t.Fatalf("invalid parent began sending: %s %v", state, err)
	}
}

func TestPostgresChildAdvanceRechecksAdmissionAndParentEvidence(t *testing.T) {
	for _, fault := range []string{"deadline", "closed attempt", "catalog", "parent evidence", "epoch"} {
		t.Run(fault, func(t *testing.T) {
			a, parent, old := formatterParentFixture(t)
			ctx := context.Background()
			var err error
			expectedToken := old.Token
			switch fault {
			case "deadline":
				_, err = a.db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 second'`)
			case "closed attempt":
				err = a.CloseAttempt(ctx, parent.Attempt.AttemptID)
			case "catalog":
				a.cfg.Catalog, err = NewCatalog(nil)
			case "epoch":
				expectedToken = "revoked-parent-epoch"
				_, err = a.db.Exec(`UPDATE budget_inbox SET claim_token=$1`, expectedToken)
			case "parent evidence":
				artifact, e := a.LookupModelReduction(ctx, parent.ActionID)
				if e != nil {
					t.Fatal(e)
				}
				artifact.Source.Evidence = append(artifact.Source.Evidence, review.EvidenceItem{Source: "forged"})
				raw, e := json.Marshal(artifact)
				if e != nil {
					t.Fatal(e)
				}
				_, err = a.db.Exec(`UPDATE budget_model_reductions SET artifact=$1,artifact_digest=$2`, raw, hash(raw))
			}
			if err != nil {
				t.Fatal(err)
			}
			if child, err := a.AdvanceModelContinuation(ctx, parent.ActionID, pipeline.ModelContinuationV1{}); err == nil || child.ID != "" {
				t.Fatalf("invalid parent advanced: %#v %v", child, err)
			}
			var token, grant string
			var actions, holds, links int
			if err = a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&token, &grant); err != nil {
				t.Fatal(err)
			}
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions); err != nil {
				t.Fatal(err)
			}
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil {
				t.Fatal(err)
			}
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_model_continuations`).Scan(&links); err != nil {
				t.Fatal(err)
			}
			if token != expectedToken || grant != parent.ActionID || actions != 1 || holds != 3 || links != 0 {
				t.Fatal("failed continuation changed authority or accounting")
			}
		})
	}
}

type faultyContinuationBuilder string

func (fault faultyContinuationBuilder) BuildModelContinuation(parent review.ModelReduction) (orchestrator.ModelContinuationPlan, error) {
	if fault == "panic" {
		panic("builder failed")
	}
	plan, err := (pipeline.ModelContinuationV1{}).BuildModelContinuation(parent)
	if err != nil {
		return plan, err
	}
	switch fault {
	case "route":
		plan.Intent.Model = "unqualified-model"
	case "parent":
		cp, _ := review.DecodeModelCheckpoint(plan.Intent.ModelCheckpoint)
		cp.Source.Evidence = append(cp.Source.Evidence, review.EvidenceItem{Source: "forged"})
		plan.Intent.ModelCheckpoint, _ = json.Marshal(cp)
	case "authority":
		plan.Intent.Job = &review.JobClaim{RunID: parent.Source.Run.ID, Token: "forged"}
	}
	return plan, nil
}

func TestPostgresChildBuilderCannotWidenOrRewriteParentGrant(t *testing.T) {
	for _, fault := range []faultyContinuationBuilder{"route", "parent", "authority", "panic"} {
		t.Run(string(fault), func(t *testing.T) {
			a, parent, old := formatterParentFixture(t)
			if p, err := a.AdvanceModelContinuation(context.Background(), parent.ActionID, fault); err == nil || p.ID != "" {
				t.Fatalf("bad builder granted a child: %#v %v", p, err)
			}
			var token, grant string
			var actions, holds int
			if err := a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&token, &grant); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions); err != nil {
				t.Fatal(err)
			}
			if err := a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil {
				t.Fatal(err)
			}
			if token != old.Token || grant != parent.ActionID || actions != 1 || holds != 3 {
				t.Fatal("bad builder changed ownership or spending")
			}
		})
	}
}
