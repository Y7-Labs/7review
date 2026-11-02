package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/Y4NN777/7review/agent/review"
)

type modelBatchAuthority interface {
	PrepareModelBatch(context.Context, []DispatchIntent) ([]PreparedAction, error)
}

// Canonical multi-batch execution hands off a complete immutable set. No live
// frame may issue an individual call or regain whole-job completion authority.
func (o *Orchestrator) handoffModelBatch(ctx context.Context, rc *review.Context, roleCfg RoleConfig, system string, batches [][]review.FileDiff, build func([]review.FileDiff) string, tools []ToolDefinition) error {
	deny := func(err error) error { return &budgetError{err} }
	if err := ctx.Err(); err != nil {
		return deny(err)
	}
	claim := review.JobClaimFromContext(ctx)
	if claim == nil || claim.RunID != rc.Source.Run.ID || claim.Token == "" || len(batches) < 2 || len(batches) > 128 || build == nil {
		return deny(errors.New("canonical batch ownership or bounded plan missing"))
	}
	a, ok := o.budgetAuthority.(modelBatchAuthority)
	if !ok {
		return deny(errors.New("atomic model batch authority unavailable"))
	}
	cp, err := review.DecodeModelCheckpoint(review.ModelCheckpointFromContext(ctx))
	if err != nil {
		return deny(err)
	}
	if cp.Cursor.Phase != review.ModelReviewBatches || cp.Cursor.BatchCount != len(batches) || cp.Cursor.BatchIndex != 0 || cp.Validation == nil || cp.Source.Diff == nil || cp.Routing == nil {
		return deny(errors.New("qualified frozen batch checkpoint unavailable"))
	}
	chain, ok := cp.Routing.Roles[string(RoleReasoner)]
	if !ok {
		return deny(errors.New("frozen reasoner routes unavailable"))
	}
	frozen, err := json.Marshal(struct {
		Batches [][]review.FileDiff
		Tools   []ToolDefinition
	}{batches, tools})
	if err != nil || len(frozen) > 64<<20 {
		return deny(errors.New("batch arguments exceed immutable bound"))
	}
	var arguments struct {
		Batches [][]review.FileDiff
		Tools   []ToolDefinition
	}
	decoder := json.NewDecoder(bytes.NewReader(frozen))
	decoder.UseNumber()
	if err = decoder.Decode(&arguments); err != nil {
		return deny(err)
	}
	var files []review.FileDiff
	for _, batch := range arguments.Batches {
		if len(batch) == 0 {
			return deny(errors.New("empty canonical batch slot"))
		}
		files = append(files, batch...)
	}
	actual, _ := json.Marshal(files)
	expected, _ := json.Marshal(cp.Source.Diff.Files)
	if !bytes.Equal(actual, expected) {
		return deny(errors.New("batch partition changed the frozen diff"))
	}
	source, _ := json.Marshal(cp.Source.Clone())
	live, err := json.Marshal(rc.Clone().Source)
	if err != nil || !bytes.Equal(source, live) {
		return deny(errors.New("live source changed after batch checkpoint"))
	}
	routes := make([]ModelSpec, 0, len(chain.Routes))
	for _, route := range chain.Routes {
		routes = append(routes, ModelSpec{Provider: route.Provider, Model: route.Model})
	}
	threshold := 0
	if roleCfg.Parallel {
		threshold = 2000
	}
	intents := make([]DispatchIntent, 0, len(arguments.Batches))
	frozenContext := &review.Context{Source: cp.Source.Clone()}
	for index, batch := range arguments.Batches {
		spec, provider, ok := o.pickRegisteredSpecForBatch(routes, batch, threshold)
		if !ok || provider == nil {
			return deny(errors.New("frozen batch route has no registered provider"))
		}
		batchCtx, err := review.WithModelBatch(ctx, index, len(arguments.Batches))
		if err != nil {
			return deny(err)
		}
		input := append([]review.FileDiff(nil), batch...)
		userMessage := build(input)
		if !slices.Equal(input, batch) {
			return deny(errors.New("batch prompt builder changed the frozen partition"))
		}
		req := LLMRequest{Model: spec.Model, SystemPrompt: system, UserMessage: userMessage, MaxTokens: chain.MaxTokens, Tools: arguments.Tools}
		i, _, err := o.newDispatchIntent(batchCtx, frozenContext, spec, RoleReasoner, fmt.Sprintf("step5_batch%d", index+1), req, "complete")
		if err != nil {
			return deny(err)
		}
		if len(i.Payload) > 1<<20 {
			return deny(errors.New("batch request exceeds storage bound"))
		}
		if err = ValidateModelCheckpoint(i); err != nil {
			return deny(err)
		}
		intents = append(intents, i)
	}
	live, err = json.Marshal(rc.Clone().Source)
	if err != nil || !bytes.Equal(source, live) {
		return deny(errors.New("batch prompt builder changed the frozen source"))
	}
	prepared, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		return deny(fmt.Errorf("atomic batch preparation denied: %w", err))
	}
	if len(prepared) != len(intents) {
		return deny(errors.New("incomplete atomic batch receipt"))
	}
	for index, p := range prepared {
		if p.ID != intents[index].ActionID || p.RequestDigest != intents[index].RequestDigest || p.Fence != 0 || p.AlreadyStarted {
			return deny(errors.New("invalid atomic batch receipt"))
		}
	}
	return ErrDurableContinuation
}
