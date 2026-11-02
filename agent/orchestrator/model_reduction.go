package orchestrator

import (
	"context"
	"github.com/Y4NN777/7review/agent/review"
)

type RecordedModelResult struct {
	Intent  DispatchIntent
	Outcome DispatchOutcome
}

type ModelResultReducer interface {
	ReduceModelResults(context.Context, []RecordedModelResult) (review.ModelReduction, error)
}

// ModelToolObserver renders an already verified parent's captured snapshot.
// The caller still owns currentness, accounting and durable commit checks.
type ModelToolObserver interface {
	ObserveModelTools(context.Context, review.ModelReduction) (review.ModelToolObservations, error)
}

type ModelContinuationPlan struct {
	Version                              int
	ParentGroupDigest, ParentInputDigest string
	Intent                               DispatchIntent
}

type ModelContinuationBuilder interface {
	BuildModelContinuation(review.ModelReduction) (ModelContinuationPlan, error)
}
