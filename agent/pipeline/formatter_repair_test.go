package pipeline

import (
	"context"
	"testing"

	"github.com/Y4NN777/7review/agent/policy"
)

func TestFormatterToolRequestCannotBecomeSuccessfulEmptyFindingsRepair(t *testing.T) {
	raw := `{"tool_requests":[{"name":"get_changed_files","input":{}}]}`
	_, status := parseFindingsDetailed(raw)
	if findings, ok := parseRepairedFindings(raw, status); ok {
		t.Fatalf("formatter tool request became repaired review: status=%s findings=%#v", status, findings)
	}
}

func TestExhaustedToolLoopCannotCompleteAsSuccessfulDraft(t *testing.T) {
	req, scm := trustedPolicyFixture()
	raw := `{"findings":[],"tool_requests":[{"name":"get_changed_files","input":{}}]}`
	model := &sequenceLLMProvider{responses: []string{raw, raw, raw, raw}}
	store := NewMemoryRunStore()
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{Mode: "enforce", Enforced: true, Trigger: policy.TriggerDecision{Accepted: true}, Effective: &policy.EffectivePolicy{AllowedTools: []string{"repo.read", "model.review"}}})
	err := p.Run(context.Background(), req)
	run, getErr := store.Get(context.Background(), "owner/repository!7")
	if err == nil || getErr != nil || run.Status != StatusFailed || model.calls != 4 || run.Source == nil || run.Source.Model.ParseStatus != "tool_loop_incomplete" {
		t.Fatalf("incomplete tool review was completed: calls=%d run=%#v err=%v get=%v", model.calls, run, err, getErr)
	}
}
