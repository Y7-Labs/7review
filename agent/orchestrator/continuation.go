package orchestrator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

var ErrDurableContinuation = errors.New("model execution handed to durable continuation")

type modelReductionAuthority interface {
	RecoverActionResults(context.Context, int) (int, error)
	RecoverModelReductions(context.Context, int, ModelResultReducer) (int, error)
	LookupModelReduction(context.Context, string) (review.ModelReduction, error)
}

type modelContinuationAuthority interface {
	modelReductionAuthority
	AdvanceModelContinuation(context.Context, string, ModelContinuationBuilder) (PreparedAction, error)
}

type modelToolContinuationAuthority interface {
	modelReductionAuthority
	CommitModelToolObservations(context.Context, string, ModelToolObserver) (*review.ModelToolObservations, error)
	AdvanceModelToolContinuation(context.Context, string, ModelToolContinuationBuilder) (PreparedAction, error)
}

// HandoffModelContinuation moves canonical live execution to the same durable
// cursor as recovery. The abandoned frame must neither send nor finish its job.
func (o *Orchestrator) HandoffModelContinuation(ctx context.Context, rc *review.Context, want review.ModelReductionState, reducer ModelResultReducer, builder ModelContinuationBuilder) error {
	if builder == nil {
		return &budgetError{errors.New("continuation builder unavailable")}
	}
	return o.handoffModelParent(ctx, rc, want, reducer, func(ctx context.Context, parent review.ModelReduction, id string) (PreparedAction, error) {
		a, ok := o.budgetAuthority.(modelContinuationAuthority)
		if !ok {
			return PreparedAction{}, errors.New("durable continuation authority unavailable")
		}
		return a.AdvanceModelContinuation(ctx, id, builder)
	})
}

func (o *Orchestrator) HandoffModelTools(ctx context.Context, rc *review.Context, reducer ModelResultReducer, observer ModelToolObserver, builder ModelToolContinuationBuilder) error {
	if observer == nil || builder == nil {
		return &budgetError{errors.New("qualified tool handoff dependencies unavailable")}
	}
	return o.handoffModelParent(ctx, rc, review.ModelReductionNeedsTools, reducer, func(ctx context.Context, parent review.ModelReduction, id string) (PreparedAction, error) {
		a, ok := o.budgetAuthority.(modelToolContinuationAuthority)
		if !ok {
			return PreparedAction{}, errors.New("durable tool continuation authority unavailable")
		}
		observations, err := a.CommitModelToolObservations(ctx, id, observer)
		if err != nil {
			return PreparedAction{}, err
		}
		if observations == nil || observations.Version != 1 || observations.RunID != parent.Source.Run.ID || observations.AttemptID != parent.Source.Attempt.AttemptID || observations.ParentGroupDigest != parent.GroupDigest || observations.ParentInputDigest != parent.InputDigest {
			return PreparedAction{}, errors.New("invalid durable observation receipt")
		}
		return a.AdvanceModelToolContinuation(ctx, id, builder)
	})
}

func (o *Orchestrator) handoffModelParent(ctx context.Context, rc *review.Context, want review.ModelReductionState, reducer ModelResultReducer, advance func(context.Context, review.ModelReduction, string) (PreparedAction, error)) error {
	deny := func(err error) error { return &budgetError{err} }
	if o == nil || rc == nil || rc.Source.Execution.DeadlineAt.IsZero() || reducer == nil {
		return deny(errors.New("durable continuation context unavailable"))
	}
	claim := review.JobClaimFromContext(ctx)
	if claim == nil || claim.RunID != rc.Source.Run.ID || claim.Token == "" || rc.Source.Execution.Mode != review.ExecutionService || rc.Source.Execution.Persistence != review.PersistenceDurableService {
		return deny(errors.New("canonical live job context required for handoff"))
	}
	ref := rc.LatestModelResult()
	if ref == nil || ref.ActionID == "" {
		return deny(errors.New("committed parent model reference unavailable"))
	}
	// A recorded canonical result is recoverable authority-owned work even when
	// indexing/advancement fails. Never send the old frame into ordinary failure
	// completion; its original token must not terminate or replay that work.
	deny = func(err error) error { return errors.Join(ErrDurableContinuation, &budgetError{err}) }
	a, ok := o.budgetAuthority.(modelReductionAuthority)
	if !ok {
		return deny(errors.New("durable reduction authority unavailable"))
	}
	runCtx, cancel := context.WithDeadline(ctx, rc.Source.Execution.DeadlineAt)
	defer cancel()
	for {
		if err := runCtx.Err(); err != nil {
			return deny(err)
		}
		if _, err := a.RecoverActionResults(runCtx, 128); err != nil {
			return deny(err)
		}
		if _, err := a.RecoverModelReductions(runCtx, 128, reducer); err != nil {
			return deny(err)
		}
		parent, err := a.LookupModelReduction(runCtx, ref.ActionID)
		if errors.Is(err, sql.ErrNoRows) {
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-runCtx.Done():
				timer.Stop()
				return deny(runCtx.Err())
			case <-timer.C:
				continue
			}
		}
		if err != nil {
			return deny(err)
		}
		matches := false
		for _, input := range parent.Inputs {
			if input.ActionID == ref.ActionID && input.RequestDigest == ref.RequestDigest && input.ResponseDigest == ref.ResponseDigest {
				matches = true
			}
		}
		actual, _ := json.Marshal(parent.Source.Execution)
		expected, _ := json.Marshal(rc.Source.Execution)
		if !matches || parent.State != want || parent.Source.Run.ID != rc.Source.Run.ID || parent.Source.Attempt.AttemptID != rc.Source.Attempt.AttemptID || parent.Source.Policy.Digest != rc.Source.Policy.Digest || !bytes.Equal(actual, expected) {
			return deny(errors.New("durable parent does not match live continuation"))
		}
		child, err := advance(runCtx, parent, ref.ActionID)
		if err != nil {
			return deny(err)
		}
		if child.ID == "" || child.RequestDigest == "" {
			return deny(errors.New("invalid durable continuation receipt"))
		}
		return ErrDurableContinuation
	}
}
