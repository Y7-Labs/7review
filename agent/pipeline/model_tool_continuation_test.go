package pipeline

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func toolContinuationFixture(t *testing.T) (review.ModelReduction, review.ModelToolObservations) {
	t.Helper()
	parent := toolObservationParent(t)
	parent.Routing = &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{"reasoner": {Routes: []review.ModelRoute{{Provider: "original-provider", Model: "original-model"}}, MaxTokens: 23}}}
	observations, err := (ModelToolObserverV1{}).ObserveModelTools(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	return parent, observations
}

func TestToolContinuationBuildsFrozenReasonerRequestWithoutAuthority(t *testing.T) {
	parent, observations := toolContinuationFixture(t)
	beforeParent, beforeObservations := mustToolJSON(t, parent), mustToolJSON(t, observations)
	plan, err := (ModelToolContinuationV1{}).BuildModelToolContinuation(parent, observations)
	if err != nil {
		t.Fatal(err)
	}
	var request orchestrator.LLMRequest
	if err := json.Unmarshal(plan.Intent.Payload, &request); err != nil {
		t.Fatal(err)
	}
	cp, err := review.DecodeModelCheckpoint(plan.Intent.ModelCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != 1 || plan.ParentGroupDigest != parent.GroupDigest || plan.ParentInputDigest != parent.InputDigest || plan.Intent.ActionID != "" || plan.Intent.Job != nil || plan.Intent.Role != "reasoner" || plan.Intent.Provider != "original-provider" || plan.Intent.Model != "original-model" || request.MaxTokens != 23 || request.Model != plan.Intent.Model || plan.Intent.Step != "tool_augmented_review_round_1" {
		t.Fatalf("tool child changed route or gained authority: %#v %#v", plan, request)
	}
	want := parent.Source.Clone()
	want.ToolRequests = append(want.ToolRequests, observations.Requests...)
	want.ToolObservations = append(want.ToolObservations, observations.Observations...)
	if !reflect.DeepEqual(cp.Source, want) || !reflect.DeepEqual(cp.Cursor, *parent.NextCursor) || !reflect.DeepEqual(cp.RawResponses, parent.RawResponses) || request.UserMessage != reviewToolFollowupUserMessage(&review.Context{Source: want}, 1, observations.Observations) || request.SystemPrompt != reviewSystemPrompt(&review.Context{Source: want}) || !reflect.DeepEqual(request.Tools, reasonerToolDefinitions(&review.Context{Source: want})) {
		t.Fatal("tool child changed evidence, tools or prompts")
	}
	if string(beforeParent) != string(mustToolJSON(t, parent)) || string(beforeObservations) != string(mustToolJSON(t, observations)) {
		t.Fatal("builder mutated inputs")
	}
}

func TestToolContinuationRejectsUnboundOrUnqualifiedInputs(t *testing.T) {
	for _, fault := range []string{"state", "observations", "round", "route", "role", "model_permission", "report"} {
		t.Run(fault, func(t *testing.T) {
			parent, observations := toolContinuationFixture(t)
			switch fault {
			case "state":
				parent.State = review.ModelReductionValidated
			case "observations":
				observations.ParentInputDigest = "changed"
			case "round":
				parent.NextCursor.Round = review.MaxModelToolRounds + 1
			case "route":
				parent.Routing = nil
			case "role":
				delete(parent.Routing.Roles, "reasoner")
			case "model_permission":
				parent.Source.Policy.Mode = "enforce"
				parent.Source.Policy.Enforced = true
				parent.Source.Policy.AllowedCapabilities = []string{"repo.read"}
			case "report":
				parent.Source.Report.Final = "unapproved"
			}
			plan, err := (ModelToolContinuationV1{}).BuildModelToolContinuation(parent, observations)
			if err == nil || plan.Intent.RequestDigest != "" {
				t.Fatalf("%s produced an unqualified child: %#v %v", fault, plan, err)
			}
		})
	}
}

func TestToolContinuationFinalRoundRetainsIncompleteEvidence(t *testing.T) {
	parent := toolObservationParentWithFault(t, "round_three")
	parent.Routing = &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{"reasoner": {Routes: []review.ModelRoute{{Provider: "original", Model: "pinned"}}, MaxTokens: 20}}}
	observations, err := (ModelToolObserverV1{}).ObserveModelTools(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := (ModelToolContinuationV1{}).BuildModelToolContinuation(parent, observations)
	if err != nil {
		t.Fatal(err)
	}
	var request orchestrator.LLMRequest
	if err := json.Unmarshal(plan.Intent.Payload, &request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(request.UserMessage, "final allowed tool round") || plan.Intent.Step != "tool_augmented_review_round_3" {
		t.Fatal("last-round bound was omitted")
	}
}
