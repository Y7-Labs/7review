package orchestrator

import (
	"context"
	"testing"
)

type recoveryStreamProvider struct{ budgetProvider }

func (p recoveryStreamProvider) Stream(ctx context.Context, req LLMRequest, emit StreamHandler) error {
	return emit("[]")
}

func TestDispatchPersistsExactInvocationMode(t *testing.T) {
	for _, stream := range []bool{false, true} {
		limits := budgetTestLimits()
		a := &recordingBudget{}
		p := recoveryStreamProvider{budgetProvider{call: func(context.Context, LLMRequest) (string, error) { return "[]", nil }}}
		o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": p}, WithOperatorCeilings(&limits), WithBudgetAuthority(a))
		var err error
		want := "complete"
		if stream {
			want = "stream"
			_, err = o.StreamComplete(context.Background(), fundedContext(limits), RoleReasoner, "test", "sys", "user", func(string) error { return nil })
		} else {
			_, err = o.Complete(context.Background(), fundedContext(limits), RoleReasoner, "test", "sys", "user")
		}
		if err != nil || len(a.intents) != 1 {
			t.Fatalf("dispatch: %v intents=%d", err, len(a.intents))
		}
		if a.intents[0].Invocation != want {
			t.Fatalf("invocation=%q want=%q", a.intents[0].Invocation, want)
		}
	}
}
