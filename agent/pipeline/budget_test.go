package pipeline

import (
	"context"
	"errors"
	"fmt"
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
	if run.Source == nil || run.Source.Policy.Limits == nil || run.Source.Policy.OperatorCeilings == nil {
		t.Fatal("budget failure discarded admitted policy and ceilings")
	}
}

type oneCallBudget struct{ calls int }

func (b *oneCallBudget) ReserveAndBegin(_ context.Context, i orchestrator.DispatchIntent) (orchestrator.DispatchPermit, error) {
	b.calls++
	if b.calls > 1 {
		return orchestrator.DispatchPermit{}, errors.New("shared capacity exhausted")
	}
	return orchestrator.DispatchPermit{ID: "first", RequestDigest: i.RequestDigest}, nil
}
func (*oneCallBudget) RecordOutcome(context.Context, orchestrator.DispatchPermit, orchestrator.DispatchOutcome) error {
	return nil
}

func TestBudgetDenialInRepairCannotBeSwallowedOrSynthesizedAway(t *testing.T) {
	for _, response := range []string{`[]`, `this is malformed output`, `[{"id":"candidate","title":"Unchecked candidate","severity":"high","description":"Not yet validated"}]`} {
		t.Run(response, func(t *testing.T) {
			req, scm := trustedPolicyFixture()
			store := NewMemoryRunStore()
			model := &sequenceLLMProvider{responses: []string{response}}
			p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{Mode: "enforce", Enforced: true, Trigger: policy.TriggerDecision{Accepted: true}, Methods: []ResolvedPolicyMethod{{Activation: review.SkillActivation{Name: "required-core", Category: "core", Required: true, RequiredChecks: []string{"correctness"}}}}, Effective: &policy.EffectivePolicy{AllowedTools: []string{"repo.read", "model.review"}}})
			limits := policyTestLimits()
			authority := &oneCallBudget{}
			p.Orchestrator = orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "fake"), map[string]orchestrator.LLMProvider{"fake": model}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(authority))
			err := p.Run(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), "budget") || model.calls != 1 || authority.calls != 2 {
				t.Fatalf("repair denial ignored: model_calls=%d reservations=%d err=%v", model.calls, authority.calls, err)
			}
			run, getErr := store.Get(context.Background(), "owner/repository!7")
			if getErr != nil || run.Status != StatusFailed || run.Source == nil || run.Source.Report.Draft != "" || len(run.Source.Findings) != 0 {
				t.Fatalf("denied repair advanced to a draft or lost provenance: run=%#v err=%v", run, getErr)
			}
			if len(run.Source.Model.RawResponses) != 1 {
				t.Fatal("partial raw model audit discarded")
			}
		})
	}
}

type budgetCancelProvider struct{ cancel context.CancelFunc }

func (budgetCancelProvider) Name() string { return "fake" }
func (p budgetCancelProvider) Complete(ctx context.Context, _ orchestrator.LLMRequest) (string, error) {
	p.cancel()
	return "", ctx.Err()
}

type contextCheckingRunStore struct {
	RunStore
	saveErr   error
	persisted bool
}

func (s *contextCheckingRunStore) SaveContext(ctx context.Context, id string, rc *review.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("failure persistence is unbounded")
	}
	s.persisted = true
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.RunStore.SaveContext(ctx, id, rc)
}

func TestCancelledModelPreservesFailureAndSurfacesPersistenceErrors(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(fmt.Sprint(failSave), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, scm := trustedPolicyFixture()
			store := &contextCheckingRunStore{RunStore: NewMemoryRunStore()}
			if failSave {
				store.saveErr = errors.New("journal save unavailable")
			}
			p := policyExecutionPipeline(store, budgetCancelProvider{cancel}, scm, TrustedPolicyResult{Mode: "enforce", Enforced: true, Trigger: policy.TriggerDecision{Accepted: true}, Effective: &policy.EffectivePolicy{AllowedTools: []string{"repo.read", "model.review"}}})
			err := p.Run(ctx, req)
			if !errors.Is(err, context.Canceled) || !store.persisted {
				t.Fatalf("cancelled run dropped failure snapshot: persisted=%v err=%v", store.persisted, err)
			}
			if failSave && !errors.Is(err, store.saveErr) {
				t.Fatalf("persistence error hidden: %v", err)
			}
			run, getErr := store.Get(context.Background(), "owner/repository!7")
			if getErr != nil || run.Status != StatusFailed || (!failSave && run.Source == nil) {
				t.Fatalf("failure status/provenance lost: run=%#v err=%v", run, getErr)
			}
		})
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
