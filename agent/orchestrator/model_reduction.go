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

type ModelContinuationPlan struct {
	Version                              int
	ParentGroupDigest, ParentInputDigest string
	Intent                               DispatchIntent
}

type ModelContinuationBuilder interface {
	BuildModelContinuation(review.ModelReduction) (ModelContinuationPlan, error)
}
