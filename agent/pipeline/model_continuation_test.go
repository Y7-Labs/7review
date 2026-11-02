package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func TestFormatterContinuationUsesFrozenRouteAndExactParentContext(t *testing.T) {
	results := recordedReducerFixture(t, 1)
	cp, _ := review.DecodeModelCheckpoint(results[0].Intent.ModelCheckpoint)
	cp.Routing = &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{
		"reasoner":  {Routes: []review.ModelRoute{{Provider: "fake", Model: "pinned"}}, MaxTokens: 20},
		"formatter": {Routes: []review.ModelRoute{{Provider: "original-formatter", Model: "formatter-pinned"}}, MaxTokens: 17},
	}}
	results[0].Intent.ModelCheckpoint, _ = json.Marshal(cp)
	raw := []byte("malformed original response")
	results[0].Outcome.Response = raw
	results[0].Outcome.ResponseDigest = reducerDigest(raw)
	parent, err := (ModelReducerV1{}).ReduceModelResults(context.Background(), results)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (ModelContinuationV1{}).BuildModelContinuation(parent)
	if err != nil {
		t.Fatal(err)
	}
	var req orchestrator.LLMRequest
	if err = json.Unmarshal(plan.Intent.Payload, &req); err != nil {
		t.Fatal(err)
	}
	if plan.Version != 1 || plan.ParentGroupDigest != parent.GroupDigest || plan.ParentInputDigest != parent.InputDigest || plan.Intent.Provider != "original-formatter" || plan.Intent.Model != "formatter-pinned" || req.MaxTokens != 17 || req.UserMessage != repairFindingsUserMessage(string(raw)) || req.SystemPrompt != repairFindingsSystemPrompt() || len(req.Tools) != 0 || plan.Intent.Job != nil || plan.Intent.ActionID != "" {
		t.Fatalf("continuation rerouted or granted authority: %#v request=%#v", plan, req)
	}
	next, err := review.DecodeModelCheckpoint(plan.Intent.ModelCheckpoint)
	if err != nil || next.Cursor.Phase != review.ModelRepairFindings || len(next.RawResponses) != 1 || next.RawResponses[0] != string(raw) || next.Validation.MinConfidence != 0.8 {
		t.Fatalf("parent context changed: %#v %v", next, err)
	}
	parent.Routing = nil
	if _, err = (ModelContinuationV1{}).BuildModelContinuation(parent); err == nil {
		t.Fatal("legacy checkpoint guessed current formatter route")
	}
}
