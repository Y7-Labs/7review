package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func toolObservationParent(t *testing.T) review.ModelReduction {
	return toolObservationParentWithFault(t, "")
}

type cancellingObservationContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancellingObservationContext) Err() error {
	c.checks++
	if c.checks == 3 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestModelToolObserverInterruptionReturnsNoPartialSet(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancellingObservationContext{Context: base, cancel: cancel}
	result, err := (ModelToolObserverV1{}).ObserveModelTools(ctx, toolObservationParent(t))
	if !errors.Is(err, context.Canceled) || result.Version != 0 || len(result.Observations) != 0 || ctx.checks != 3 {
		t.Fatalf("interrupted local rendering exposed partial success: %#v checks=%d %v", result, ctx.checks, err)
	}
}

func toolObservationParentWithFault(t *testing.T, fault string) review.ModelReduction {
	t.Helper()
	results := recordedReducerFixture(t, 1)
	cp, err := review.DecodeModelCheckpoint(results[0].Intent.ModelCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	cp.Source.SkillActivations = []review.SkillActivation{{Name: "snapshot-reader", AllowedTools: []string{"scm-api", "diff-analyzer", "corpus-selector"}}}
	requests := []review.ToolRequest{{Name: "get_changed_files", Input: map[string]any{"run": cp.Source.Run.ID}}, {Name: "get_diff_summary"}}
	switch fault {
	case "round_two", "round_three":
		previous := 1
		if fault == "round_three" {
			previous = 2
		}
		cp.Cursor = review.ModelCursor{Phase: review.ModelToolRound, Round: previous}
		for index := 0; index < previous; index++ {
			cp.RawResponses = append(cp.RawResponses, `{"tool_requests":[{"name":"get_inline_positions"}]}`)
		}
		results[0].Intent.Step = fmt.Sprintf("tool_augmented_review_round_%d", previous)
	case "policy":
		cp.Source.Policy.AllowedCapabilities = []string{"model.review"}
	case "skill":
		cp.Source.SkillActivations = nil
	case "foreign_run":
		requests[0].Input["run"] = "foreign"
	case "unknown_field":
		requests[0].Input = map[string]any{"path": "foreign"}
	case "unknown_tool":
		requests[0].Name = "publish_final"
	case "truncated":
		requests[0].Name = "get_merge_request"
		cp.Source.SCM.Description = strings.Repeat("\u754c", 5000)
	}
	results[0].Intent.ModelCheckpoint = mustToolJSON(t, cp)
	raw := mustToolJSON(t, map[string]any{"tool_requests": requests})
	results[0].Outcome.Response = raw
	results[0].Outcome.ResponseDigest = modelDigest(raw)
	parent, err := (ModelReducerV1{}).ReduceModelResults(context.Background(), results)
	if err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestModelToolObserverDoesNotReplayEarlierRoundProposals(t *testing.T) {
	for _, fault := range []string{"round_two", "round_three"} {
		t.Run(fault, func(t *testing.T) {
			parent := toolObservationParentWithFault(t, fault)
			result, err := (ModelToolObserverV1{}).ObserveModelTools(context.Background(), parent)
			if err != nil || len(result.Observations) != 2 {
				t.Fatalf("current round was merged with prior proposals: %#v %v", result, err)
			}
			for _, observation := range result.Observations {
				if observation.Round != parent.NextCursor.Round || observation.Name == "get_inline_positions" || observation.Status != "ok" {
					t.Fatalf("earlier round observation replayed: %#v", observation)
				}
			}
		})
	}
}

func TestModelToolObserverIsDeterministicDetachedAndNotAuthority(t *testing.T) {
	parent := toolObservationParent(t)
	before := mustToolJSON(t, parent)
	var observer orchestrator.ModelToolObserver = ModelToolObserverV1{}
	first, err := observer.ObserveModelTools(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := observer.ObserveModelTools(context.Background(), parent)
	if err != nil || !reflect.DeepEqual(first, second) || !bytes.Equal(before, mustToolJSON(t, parent)) {
		t.Fatalf("snapshot observation depends on live state or mutates parent: %v", err)
	}
	if first.Version != 1 || first.RunID != parent.Source.Run.ID || first.AttemptID != parent.Source.Attempt.AttemptID || first.ParentGroupDigest != parent.GroupDigest || first.ParentInputDigest != parent.InputDigest || first.RequestDigest != modelDigest(mustToolJSON(t, parent.ToolRequests)) || len(first.Observations) != len(parent.ToolRequests) {
		t.Fatalf("observation attribution lost: %#v", first)
	}
	for index, observation := range first.Observations {
		if observation.Status != "ok" || observation.Name != first.Requests[index].Name || observation.Round != parent.NextCursor.Round || !json.Valid([]byte(observation.Result)) {
			t.Fatalf("snapshot read was fabricated: %#v", observation)
		}
	}
	first.Requests[0].Input["run"] = "mutated"
	if !bytes.Equal(before, mustToolJSON(t, parent)) {
		t.Fatal("output retained a mutable parent input")
	}
	if parent.Source.Report.Draft != "" || parent.Source.Report.Final != "" || len(parent.Source.ToolObservations) != 0 {
		t.Fatal("local adapter gained continuation or publication authority")
	}
}

func TestModelToolObserverRejectsUnqualifiedOrInterruptedPlans(t *testing.T) {
	for _, fault := range []string{"version", "state", "group", "inputs", "round", "parent_cursor", "diff", "request_round", "count", "attestation", "final", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			parent := toolObservationParent(t)
			ctx := context.Background()
			switch fault {
			case "version":
				parent.Version = 99
			case "state":
				parent.State = review.ModelReductionValidated
			case "group":
				parent.GroupDigest = "unbound"
			case "inputs":
				parent.InputDigest = "unbound"
			case "round":
				parent.NextCursor.Round = review.MaxModelToolRounds + 1
			case "parent_cursor":
				parent.Cursor = review.ModelCursor{Phase: review.ModelToolRound}
			case "diff":
				parent.Source.Diff = nil
			case "request_round":
				parent.ToolRequests[0].Round++
			case "count":
				for len(parent.ToolRequests) <= review.MaxModelToolRequests {
					parent.ToolRequests = append(parent.ToolRequests, parent.ToolRequests[0])
				}
			case "attestation":
				parent.Source.Attestation = nil
			case "final":
				parent.Source.Report.Final = "not authorized"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := (ModelToolObserverV1{}).ObserveModelTools(ctx, parent)
			if err == nil || result.Version != 0 || len(result.Observations) != 0 {
				t.Fatalf("invalid parent produced a partial observation receipt: %#v %v", result, err)
			}
		})
	}
}

func TestModelToolObserverPreservesDenialsAndTruncation(t *testing.T) {
	for _, fault := range []string{"policy", "skill", "foreign_run", "unknown_field", "unknown_tool", "truncated"} {
		t.Run(fault, func(t *testing.T) {
			parent := toolObservationParentWithFault(t, fault)
			result, err := (ModelToolObserverV1{}).ObserveModelTools(context.Background(), parent)
			if err != nil {
				t.Fatal(err)
			}
			want := "denied"
			if fault == "truncated" {
				want = "truncated"
			}
			if result.Observations[0].Status != want || (want == "denied" && result.Observations[0].Result != "") {
				t.Fatalf("unavailable evidence became success: %#v", result.Observations[0])
			}
			for _, observation := range result.Observations {
				if len(mustToolJSON(t, observation)) > review.MaxModelToolObservationBytes {
					t.Fatal("observation exceeded qualified bound")
				}
			}
		})
	}
}

func TestModelToolObserverRejectsLegacyOmittedOrAlteredProposals(t *testing.T) {
	for _, fault := range []string{"legacy_omission", "altered_request", "altered_response"} {
		t.Run(fault, func(t *testing.T) {
			parent := toolObservationParent(t)
			switch fault {
			case "altered_request":
				parent.ToolRequests[0].Input["run"] = "foreign"
			case "altered_response":
				parent.RawResponses[len(parent.RawResponses)-1] = "[]"
			case "legacy_omission":
				parent.ToolRequests = nil
				for index := 0; index < review.MaxModelToolRequests; index++ {
					parent.ToolRequests = append(parent.ToolRequests, review.ToolRequest{Name: "get_changed_files", Round: 1})
				}
				requests := append(append([]review.ToolRequest(nil), parent.ToolRequests...), parent.ToolRequests[0])
				raw := mustToolJSON(t, map[string]any{"tool_requests": requests})
				parent.RawResponses[len(parent.RawResponses)-1] = string(raw)
				parent.Inputs[0].ResponseDigest = modelDigest(raw)
				parent.InputDigest = modelDigest(mustToolJSON(t, parent.Inputs))
			}
			result, err := (ModelToolObserverV1{}).ObserveModelTools(context.Background(), parent)
			if err == nil || result.Version != 0 || len(result.Observations) != 0 {
				t.Fatalf("inconsistent raw plan gained observation receipt: %#v %v", result, err)
			}
		})
	}
}
