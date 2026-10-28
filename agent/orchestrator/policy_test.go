package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/review"
)

type forbiddenPolicyProvider struct{ t *testing.T }

func (p forbiddenPolicyProvider) Name() string { return "fake" }
func (p forbiddenPolicyProvider) Complete(context.Context, llm.LLMRequest) (string, error) {
	p.t.Error("policy-denied request reached provider")
	return "unexpected", nil
}
func (p forbiddenPolicyProvider) Stream(context.Context, llm.LLMRequest, llm.StreamHandler) error {
	p.t.Error("policy-denied request reached streaming provider")
	return nil
}

func TestPolicyDenialCoversEveryModelDispatchPath(t *testing.T) {
	for _, mode := range []string{"complete", "stream", "parallel"} {
		t.Run(mode, func(t *testing.T) {
			o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": forbiddenPolicyProvider{t}})
			rc := review.NewContext(review.Request{})
			rc.Source.Policy.Enforced = true
			var err error
			switch mode {
			case "complete":
				_, err = o.Complete(context.Background(), rc, RoleReasoner, "denied", "system", "user")
			case "stream":
				_, err = o.StreamComplete(context.Background(), rc, RoleFormatter, "denied", "system", "user", func(string) error {
					t.Error("denied stream emitted data")
					return nil
				})
			case "parallel":
				err = o.CompleteParallel(context.Background(), rc, "system", [][]review.FileDiff{{{Path: "a.go"}}, {{Path: "b.go"}}}, func([]review.FileDiff) string { return "user" })
			}
			if err == nil || !strings.Contains(err.Error(), "model.review") || len(rc.AllFindings()) != 0 {
				t.Fatalf("expected policy denial without output: err=%v findings=%v", err, rc.AllFindings())
			}
		})
	}
}

func TestPolicyAllowsModelInEnforcedPreviewAndLegacyModes(t *testing.T) {
	for _, policy := range []review.PolicyProjection{
		{},
		{Mode: "preview"},
		{Mode: "enforce", Enforced: true, AllowedCapabilities: []string{"model.review"}},
	} {
		t.Run(policy.Mode, func(t *testing.T) {
			o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": recordingProvider{prefix: "fake"}})
			if policy.Enforced {
				limits := budgetTestLimits()
				policy.Limits = &limits
				policy.OperatorCeilings = &limits
				policy.Digest = "test-policy"
				o = NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": recordingProvider{prefix: "fake"}}, WithOperatorCeilings(&limits), WithBudgetAuthority(&recordingBudget{}))
			}
			rc := review.NewContext(review.Request{})
			rc.Source.Policy = policy
			if _, err := o.Complete(context.Background(), rc, RoleReasoner, "allowed", "system", "user"); err != nil {
				t.Fatalf("allowed model was blocked: %v", err)
			}
		})
	}
}
