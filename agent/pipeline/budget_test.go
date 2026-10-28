package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

func TestPipelineBudgetDenialIsRecordedAsFailedWithoutModelIO(t *testing.T) {
	req, scm := trustedPolicyFixture()
	store := NewMemoryRunStore()
	model := &sequenceLLMProvider{responses: []string{`[]`}}
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{Mode: "enforce", Enforced: true, Trigger: policy.TriggerDecision{Accepted: true}, Effective: &policy.EffectivePolicy{AllowedTools: []string{"repo.read", "model.review"}}})
	p.Orchestrator = orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "fake"), map[string]orchestrator.LLMProvider{"fake": model})
	err := p.Run(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "budget") || model.calls != 0 {
		t.Fatalf("budget bypass: calls=%d err=%v", model.calls, err)
	}
	run, err := store.Get(context.Background(), "owner/repository!7")
	if err != nil || run.Status != StatusFailed || !strings.Contains(run.Error, "budget") {
		t.Fatalf("denial lost: run=%#v err=%v", run, err)
	}
}

func TestProjectedLimitsRemainDetachedFromAdmissionAndSnapshots(t *testing.T) {
	limits := policyTestLimits()
	p := projectTrustedPolicy(TrustedPolicyResult{Enforced: true, Effective: &policy.EffectivePolicy{Limits: limits, OperatorCeilings: &limits}})
	source := review.Source{Policy: p}
	snapshot := source.Clone()
	limits.Change.Period.DurationMS = 1
	source.Policy.Limits.Project.Period.DurationMS = 2
	source.Policy.OperatorCeilings.Change.Period.DurationMS = 3
	if snapshot.Policy.Limits.Project.Period.DurationMS != 2592000000 || snapshot.Policy.OperatorCeilings.Change.Period.DurationMS != 2592000000 {
		t.Fatal("mutable limits leaked across authority snapshots")
	}
}

// This fake funds unrelated policy execution fixtures only. It is not an
// accounting implementation and does not qualify price bounds or persistence.
type policyTestBudget struct{}

func (policyTestBudget) ReserveAndBegin(_ context.Context, intent orchestrator.DispatchIntent) (orchestrator.DispatchPermit, error) {
	return orchestrator.DispatchPermit{ID: "test-only", RequestDigest: intent.RequestDigest}, nil
}
func (policyTestBudget) RecordOutcome(context.Context, orchestrator.DispatchPermit, orchestrator.DispatchOutcome) error {
	return nil
}

func policyTestLimits() review.ResourceLimits {
	period := &review.FixedBudgetPeriod{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 2592000000}
	scope := review.ResourceLimitScope{ModelCalls: 1000000, ToolCalls: 1000000, InputTokens: 1000000000, OutputTokens: 1000000000, MoneyMicro: 1000000000, ActiveMS: 1000000000, ObservationBytes: 1000000000}
	limits := review.ResourceLimits{Currency: "USD", Attempt: scope, Change: scope, Project: scope}
	limits.Change.Period = period
	limits.Project.Period = period
	return limits
}

func bindPolicyTestBudget(p *review.PolicyProjection) {
	limits := policyTestLimits()
	p.Limits = &limits
	p.OperatorCeilings = &limits
	if p.Digest == "" {
		p.Digest = "test-policy"
	}
}
