package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type handoffBudget struct {
	recordingBudget
	parent   review.ModelReduction
	fault    string
	advances int
}

func (b *handoffBudget) RecoverActionResults(context.Context, int) (int, error) {
	if b.fault == "index" {
		return 0, errors.New("index unavailable")
	}
	return 0, nil
}
func (b *handoffBudget) RecoverModelReductions(context.Context, int, ModelResultReducer) (int, error) {
	return 0, nil
}
func (b *handoffBudget) LookupModelReduction(context.Context, string) (review.ModelReduction, error) {
	return b.parent, nil
}
func (b *handoffBudget) AdvanceModelContinuation(context.Context, string, ModelContinuationBuilder) (PreparedAction, error) {
	b.advances++
	if b.fault == "advance" {
		return PreparedAction{}, errors.New("advance failed")
	}
	if b.fault == "receipt" {
		return PreparedAction{}, nil
	}
	return PreparedAction{ID: "same-child", RequestDigest: "child-digest"}, nil
}

type handoffReducer struct{}

func (handoffReducer) ReduceModelResults(context.Context, []RecordedModelResult) (review.ModelReduction, error) {
	return review.ModelReduction{}, nil
}

type handoffBuilder struct{}

func (handoffBuilder) BuildModelContinuation(review.ModelReduction) (ModelContinuationPlan, error) {
	return ModelContinuationPlan{}, nil
}

func TestLiveHandoffNeverFallsBackWhenParentOrAuthorityIsInvalid(t *testing.T) {
	for _, fault := range []string{"", "index", "advance", "receipt", "parent digest", "parent state", "run", "deadline"} {
		t.Run(fault, func(t *testing.T) {
			limits := budgetTestLimits()
			rc := fundedContext(limits)
			rc.Run.ID = "run"
			rc.Attempt.AttemptID = "attempt"
			rc.Execution.DeadlineAt = time.Now().Add(time.Minute)
			rc.Execution.Mode = review.ExecutionService
			rc.Execution.Persistence = review.PersistenceDurableService
			rc.RecordModelResult(review.ModelCallRef{ActionID: "parent", RequestDigest: "request", ResponseDigest: "response"})
			a := &handoffBudget{fault: fault, parent: review.ModelReduction{State: review.ModelReductionNeedsFindingsRepair, Source: rc.Source.Clone(), Inputs: []review.ModelResultRef{{ActionID: "parent", RequestDigest: "request", ResponseDigest: "response"}}}}
			switch fault {
			case "parent digest":
				a.parent.Inputs[0].ResponseDigest = "changed"
			case "parent state":
				a.parent.State = review.ModelReductionValidated
			case "run":
				a.parent.Source.Run.ID = "other"
			case "deadline":
				a.parent.Source.Execution.DeadlineAt = a.parent.Source.Execution.DeadlineAt.Add(time.Minute)
			}
			o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), nil, WithBudgetAuthority(a))
			ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: rc.Run.ID, Token: "live-owner"})
			err := o.HandoffModelContinuation(ctx, rc, review.ModelReductionNeedsFindingsRepair, handoffReducer{}, handoffBuilder{})
			if fault == "" {
				if !errors.Is(err, ErrDurableContinuation) || a.advances != 1 {
					t.Fatalf("handoff: advances=%d err=%v", a.advances, err)
				}
			} else if err == nil || errors.Is(err, ErrDurableContinuation) || !IsBudgetError(err) {
				t.Fatalf("bad parent handed off: %v", err)
			}
			if (fault == "index" || fault == "parent digest" || fault == "parent state" || fault == "run" || fault == "deadline") && a.advances != 0 {
				t.Fatal("invalid parent reached advancement")
			}
			if len(a.intents) != 0 || len(a.outcomes) != 0 {
				t.Fatal("handoff used legacy reservation or provider path")
			}
		})
	}
}
