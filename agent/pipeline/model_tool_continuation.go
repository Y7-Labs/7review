package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

type ModelToolContinuationV1 struct{}

// BuildModelToolContinuation only constructs a proposal from frozen evidence.
// The caller must verify the durable observation receipt and current authority.
func (ModelToolContinuationV1) BuildModelToolContinuation(parent review.ModelReduction, observations review.ModelToolObservations) (orchestrator.ModelContinuationPlan, error) {
	var empty orchestrator.ModelContinuationPlan
	if _, err := observations.SnapshotUsage(parent); err != nil {
		return empty, err
	}
	if parent.Routing == nil || parent.Source.Policy.OperatorCeilings == nil || parent.Source.Report.Draft != "" || parent.Source.Report.Final != "" || !parent.Source.Policy.AllowsCapability("model.review") {
		return empty, errors.New("qualified reasoner continuation unavailable")
	}
	if err := parent.Routing.Validate(); err != nil {
		return empty, err
	}
	chain, ok := parent.Routing.Roles["reasoner"]
	if !ok || len(chain.Routes) == 0 {
		return empty, errors.New("original reasoner route unavailable")
	}
	raw, err := json.Marshal(struct {
		Parent       review.ModelReduction
		Observations review.ModelToolObservations
	}{parent, observations})
	if err != nil {
		return empty, err
	}
	var frozen struct {
		Parent       review.ModelReduction
		Observations review.ModelToolObservations
	}
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return empty, err
	}
	parent, observations = frozen.Parent, frozen.Observations
	rc := &review.Context{Source: parent.Source}
	rc.Source.ToolRequests = append(rc.Source.ToolRequests, observations.Requests...)
	rc.Source.ToolObservations = append(rc.Source.ToolObservations, observations.Observations...)
	for _, response := range parent.RawResponses {
		rc.AddFindings(response)
	}
	ctx, err := review.WithModelCheckpoint(review.WithModelRouting(review.WithModelValidation(context.Background(), &parent.Validation), parent.Routing), rc, *parent.NextCursor)
	if err != nil {
		return empty, err
	}
	route := chain.Routes[0]
	payload, err := json.Marshal(orchestrator.LLMRequest{Model: route.Model, MaxTokens: chain.MaxTokens, SystemPrompt: reviewSystemPrompt(rc), UserMessage: reviewToolFollowupUserMessage(rc, parent.NextCursor.Round, observations.Observations), Tools: reasonerToolDefinitions(rc)})
	if err != nil {
		return empty, err
	}
	s := rc.Source
	i := orchestrator.DispatchIntent{Invocation: "complete", Provider: route.Provider, Model: route.Model, Role: "reasoner", Step: fmt.Sprintf("tool_augmented_review_round_%d", parent.NextCursor.Round), Payload: payload, RequestDigest: modelDigest(payload), ModelCheckpoint: review.ModelCheckpointFromContext(ctx), Attempt: s.Attempt, Execution: s.Execution, Attestation: s.Attestation, PolicyDigest: s.Policy.Digest, PolicyLimits: s.Policy.Limits, OperatorCeilings: s.Policy.OperatorCeilings.Clone()}
	if err = orchestrator.ValidateModelCheckpoint(i); err != nil {
		return empty, err
	}
	return orchestrator.ModelContinuationPlan{Version: 1, ParentGroupDigest: parent.GroupDigest, ParentInputDigest: parent.InputDigest, Intent: i}, nil
}
