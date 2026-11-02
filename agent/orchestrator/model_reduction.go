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
