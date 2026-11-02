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

type modelContinuationAuthority interface {
	RecoverActionResults(context.Context, int) (int, error)
	RecoverModelReductions(context.Context, int, ModelResultReducer) (int, error)
	LookupModelReduction(context.Context, string) (review.ModelReduction, error)
	AdvanceModelContinuation(context.Context, string, ModelContinuationBuilder) (PreparedAction, error)
}

// HandoffModelContinuation moves canonical live execution to the same durable
// cursor as recovery. The abandoned frame must neither send nor finish its job.
func (o *Orchestrator) HandoffModelContinuation(ctx context.Context, rc *review.Context, want review.ModelReductionState, reducer ModelResultReducer, builder ModelContinuationBuilder) error {
	deny := func(err error) error { return &budgetError{err} }
	if o == nil || rc == nil || rc.Source.Execution.DeadlineAt.IsZero() || reducer == nil || builder == nil {
		return deny(errors.New("durable continuation context unavailable"))
	}
	claim := review.JobClaimFromContext(ctx)
	if claim == nil || claim.RunID != rc.Source.Run.ID || claim.Token == "" || rc.Source.Execution.Mode != review.ExecutionService || rc.Source.Execution.Persistence != review.PersistenceDurableService {
		return deny(errors.New("canonical live job context required for handoff"))
	}
	a, ok := o.budgetAuthority.(modelContinuationAuthority)
	if !ok {
		return deny(errors.New("durable continuation authority unavailable"))
	}
	ref := rc.LatestModelResult()
	if ref == nil || ref.ActionID == "" {
		return deny(errors.New("committed parent model reference unavailable"))
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
		child, err := a.AdvanceModelContinuation(runCtx, ref.ActionID, builder)
		if err != nil {
			return deny(err)
		}
		if child.ID == "" || child.RequestDigest == "" {
			return deny(errors.New("invalid durable continuation receipt"))
		}
		return ErrDurableContinuation
	}
}
