package accounting

import (
	"context"
	"sync"
	"testing"

	"github.com/Y4NN777/7review/agent/pipeline"
)

func TestOriginalLiveOwnerCannotFinishAfterRecordedModelProgress(t *testing.T) {
	for _, success := range []bool{false, true} {
		a, i, claim, _ := recordedReductionFixtureResponseMode(t, []byte(`{"tool_requests":[{"name":"get_changed_files"}]}`), ledgerLimits(), false)
		if err := a.FinishInbox(context.Background(), i.Job.RunID, claim.Token, success); err == nil {
			t.Fatal("original frame finished an effect-started job without terminal proof")
		}
		var state, phase string
		if err := a.db.QueryRow(`SELECT i.state,j.phase FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&state, &phase); err != nil || state != "running" || phase != "effect_started" {
			t.Fatalf("completion error stranded durable work: %s %s %v", state, phase, err)
		}
	}
}

func TestLiveOwnerCanFailBeforeAnyModelEffectAndCannotDispatchAfterward(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	if err := a.FinishInbox(ctx, i.Job.RunID, i.Job.Token, false); err != nil {
		t.Fatal(err)
	}
	if p, err := a.PrepareAction(ctx, i); err == nil || p.ID != "" {
		t.Fatalf("finished no-effect job regained dispatch: %#v %v", p, err)
	}
}

func TestOriginalOwnerCannotFinishCommittedPreparationBeforeSending(t *testing.T) {
	for _, success := range []bool{false, true} {
		a, i, _ := preparedActionFixture(t)
		p, err := a.PrepareAction(context.Background(), i)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.FinishInbox(context.Background(), i.Job.RunID, i.Job.Token, success); err == nil {
			t.Fatal("lost preparation acknowledgement could terminate recoverable work")
		}
		candidates, err := a.PreparedRecoveryCandidates(context.Background(), 8)
		if err != nil || len(candidates) != 1 || candidates[0].ID != p.ID {
			t.Fatalf("prepared work was stranded: %#v %v", candidates, err)
		}
	}
}

func TestPendingToolContinuationWorkersConvergeAfterObservationOnlyRestart(t *testing.T) {
	a, i := toolChildParentFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.AdvancePendingModelToolContinuations(ctx, 8, pipeline.ModelToolObserverV1{}, pipeline.ModelToolContinuationV1{})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var children, observations, holds int
	if err := a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_model_tool_children),(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_holds)`).Scan(&children, &observations, &holds); err != nil || children != 1 || observations != 1 || holds != 6 {
		t.Fatalf("workers lost or duplicated accepted observation work: %d %d %d %v", children, observations, holds, err)
	}
	if p, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{}); err != nil || p.ID == "" {
		t.Fatalf("child proof lost: %#v %v", p, err)
	}
}

func TestPendingToolBudgetDenialDoesNotBlockLaterHealthyCandidates(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	policy := ledgerLimits()
	policy.Attempt.ModelCalls = 1
	blocked := toolParentForChangeWithPolicy(t, a, "7", &policy)
	ctx := context.Background()
	if n, err := a.AdvancePendingModelToolContinuations(ctx, 1, pipeline.ModelToolObserverV1{}, pipeline.ModelToolContinuationV1{}); err != nil || n != 0 {
		t.Fatalf("budget wait blocked the whole recovery cycle: %d %v", n, err)
	}
	healthy := toolParentForChange(t, a, "8")
	for n := 0; n < 3; n++ {
		if _, err := a.AdvancePendingModelToolContinuations(ctx, 1, pipeline.ModelToolObserverV1{}, pipeline.ModelToolContinuationV1{}); err != nil {
			t.Fatal(err)
		}
	}
	var children int
	if err := db.QueryRow(`SELECT count(*) FROM budget_model_tool_children`).Scan(&children); err != nil {
		t.Fatal(err)
	}
	var action string
	if err := db.QueryRow(`SELECT x.action_id FROM budget_model_tool_children c JOIN budget_model_reduction_inputs x ON x.reduction_id=c.parent_reduction_id`).Scan(&action); err != nil || children != 1 || action != healthy.ActionID {
		t.Fatalf("old budget wait starved healthy work: children=%d action=%s %v", children, action, err)
	}
	var held int
	if err := db.QueryRow(`SELECT count(*) FROM budget_holds WHERE action_id=$1`, blocked.ActionID).Scan(&held); err != nil || held != 3 {
		t.Fatalf("budget wait refunded unknown model usage: %d %v", held, err)
	}
}
