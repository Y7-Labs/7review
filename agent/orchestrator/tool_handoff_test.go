package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type toolHandoffBudget struct {
	handoffBudget
	observes int
}

func (b *toolHandoffBudget) CommitModelToolObservations(context.Context, string, ModelToolObserver) (*review.ModelToolObservations, error) {
	b.observes++
	if b.fault == "observe" {
		return nil, errors.New("observation commit unavailable")
	}
	return &review.ModelToolObservations{Version: 1, RunID: b.parent.Source.Run.ID, AttemptID: b.parent.Source.Attempt.AttemptID, ParentGroupDigest: b.parent.GroupDigest, ParentInputDigest: b.parent.InputDigest}, nil
}
func (b *toolHandoffBudget) AdvanceModelToolContinuation(context.Context, string, ModelToolContinuationBuilder) (PreparedAction, error) {
	return b.AdvanceModelContinuation(context.Background(), "", handoffBuilder{})
}

type toolHandoffObserver struct{}

func (toolHandoffObserver) ObserveModelTools(context.Context, review.ModelReduction) (review.ModelToolObservations, error) {
	return review.ModelToolObservations{}, nil
}

type toolHandoffBuilder struct{}

func (toolHandoffBuilder) BuildModelToolContinuation(review.ModelReduction, review.ModelToolObservations) (ModelContinuationPlan, error) {
	return ModelContinuationPlan{}, nil
}

func TestToolHandoffAbandonsRecordedLiveFrameWithoutLegacyFallback(t *testing.T) {
	for _, fault := range []string{"", "index", "observe", "advance", "receipt", "parent"} {
		t.Run(fault, func(t *testing.T) {
			rc := fundedContext(budgetTestLimits())
			rc.Run.ID = "run"
			rc.Attempt.AttemptID = "attempt"
			rc.Execution.DeadlineAt = time.Now().Add(time.Minute)
			rc.Execution.Mode = review.ExecutionService
			rc.Execution.Persistence = review.PersistenceDurableService
			rc.RecordModelResult(review.ModelCallRef{ActionID: "parent", RequestDigest: "request", ResponseDigest: "response"})
			a := &toolHandoffBudget{handoffBudget: handoffBudget{fault: fault, parent: review.ModelReduction{State: review.ModelReductionNeedsTools, Source: rc.Source.Clone(), Inputs: []review.ModelResultRef{{ActionID: "parent", RequestDigest: "request", ResponseDigest: "response"}}}}}
			if fault == "parent" {
				a.parent.Inputs[0].ResponseDigest = "changed"
			}
			o := NewOrchestrator(DefaultOrchestratorConfig("current", "small", "fake"), nil, WithBudgetAuthority(a))
			ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: "run", Token: "old-owner"})
			err := o.HandoffModelTools(ctx, rc, handoffReducer{}, toolHandoffObserver{}, toolHandoffBuilder{})
			if !errors.Is(err, ErrDurableContinuation) || (fault != "" && !IsBudgetError(err)) {
				t.Fatalf("recorded live frame regained failure/completion or fallback rights: %v", err)
			}
			if fault == "" && (a.observes != 1 || a.advances != 1) {
				t.Fatal("tool handoff did not use both guarded transitions")
			}
			if (fault == "index" || fault == "parent") && (a.observes != 0 || a.advances != 0) {
				t.Fatal("unverified parent reached tool work")
			}
			if fault == "observe" && a.advances != 0 {
				t.Fatal("failed observations authorized child")
			}
			if len(a.intents) != 0 || len(a.outcomes) != 0 {
				t.Fatal("handoff used provider or legacy reservation")
			}
		})
	}
}
