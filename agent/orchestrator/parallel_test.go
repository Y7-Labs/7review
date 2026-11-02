package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/review"
)

type recordingProvider struct {
	prefix string
}

func (p recordingProvider) Name() string { return p.prefix }

func (p recordingProvider) Complete(_ context.Context, req llm.LLMRequest) (string, error) {
	return p.prefix + ":" + req.Model, nil
}

func TestSerialBatchesReviewEveryBatchAndRejectEmptyWork(t *testing.T) {
	for _, empty := range []bool{false, true} {
		cfg := DefaultOrchestratorConfig("review", "small", "fake")
		role := cfg.Roles[RoleReasoner]
		role.Parallel = false
		cfg.Roles[RoleReasoner] = role
		var seen []string
		provider := budgetProvider{call: func(_ context.Context, req LLMRequest) (string, error) {
			seen = append(seen, req.UserMessage)
			return req.UserMessage, nil
		}}
		o := NewOrchestrator(cfg, map[string]LLMProvider{"fake": provider})
		rc := review.NewContext(review.Request{})
		batches := [][]review.FileDiff{{{Path: "a.go"}}, {{Path: "b.go"}}}
		if empty {
			batches = nil
		}
		err := o.CompleteParallel(context.Background(), rc, "sys", batches, func(batch []review.FileDiff) string { return batch[0].Path })
		if empty {
			if err == nil || len(seen) != 0 {
				t.Fatalf("empty review reported coverage: %v %#v", err, seen)
			}
		} else if err != nil || strings.Join(seen, ",") != "a.go,b.go" || len(rc.AllFindings()) != 2 {
			t.Fatalf("serial review omitted batches: %v %#v", err, seen)
		}
	}
}

func TestCompleteParallelUsesRegisteredFallbackWhenPrimaryUnavailable(t *testing.T) {
	orch := NewOrchestrator(&OrchestratorConfig{
		Roles: map[ModelRole]RoleConfig{
			RoleReasoner: {
				Primary:   ModelSpec{Model: "claude-sonnet", Provider: "anthropic"},
				Fallbacks: []ModelSpec{{Model: "gpt-4o", Provider: "openai"}},
				MaxTokens: 2048,
				Parallel:  true,
			},
		},
	}, map[string]LLMProvider{
		"openai": recordingProvider{prefix: "openai"},
	})
	rc := review.NewContext(review.Request{Provider: "test"})
	err := orch.CompleteParallel(
		context.Background(),
		rc,
		"system",
		[][]review.FileDiff{
			{{Path: "a.go", TokenCount: 3000}},
			{{Path: "b.go", TokenCount: 100}},
		},
		func(batch []review.FileDiff) string {
			return batch[0].Path
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rc.AllFindings(), "\n"); strings.Count(got, "openai:gpt-4o") != 2 {
		t.Fatalf("expected both batches to use registered fallback, got %q", got)
	}
}
