package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

func TestReviewToolsDeniedByEffectivePolicyReturnNoData(t *testing.T) {
	for _, tool := range reasonerToolSchemas() {
		t.Run(tool.Name, func(t *testing.T) {
			rc := review.NewContext(review.Request{})
			rc.Source.Policy = projectTrustedPolicy(TrustedPolicyResult{
				Mode: "enforce", Enforced: true,
				Effective: &policy.EffectivePolicy{AllowedTools: []string{"model.review"}},
			})
			rc.Source.SkillActivations = []review.SkillActivation{{Name: "permissive-skill", AllowedTools: []string{tool.Surface}}}
			rc.Source.SCM = &review.SCMContext{Description: "private repository context"}
			got := (&Pipeline{}).executeReviewToolRequest(rc, review.ToolRequest{Name: tool.Name})
			if got.Status != "denied" || got.Result != "" || !strings.Contains(got.Reason, "policy") {
				t.Fatalf("skill permission bypassed effective policy: %#v", got)
			}
		})
	}
}

func TestPipelinePolicyDeniesModelBeforeProviderCall(t *testing.T) {
	req, scm := trustedPolicyFixture()
	model := &sequenceLLMProvider{responses: []string{`[]`}}
	store := NewMemoryRunStore()
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{
		Mode: "enforce", Enforced: true, Trigger: policy.TriggerDecision{Accepted: true},
		Effective: &policy.EffectivePolicy{AllowedTools: []string{"repo.read"}},
	})
	err := p.Run(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "model.review") || model.calls != 0 {
		t.Fatalf("denied model crossed provider boundary: calls=%d err=%v", model.calls, err)
	}
	run, err := store.Get(context.Background(), "owner/repository!7")
	if err != nil || run.Status != StatusFailed {
		t.Fatalf("denial was not recorded: run=%#v err=%v", run, err)
	}
}

func TestReviewToolRequiresBothPolicyAndSkillPermission(t *testing.T) {
	for _, tc := range []struct {
		name         string
		enforced     bool
		capabilities []string
		skill        bool
		want         string
	}{
		{"both allow", true, []string{"repo.read"}, true, "ok"},
		{"skill denies", true, []string{"repo.read"}, false, "denied"},
		{"empty intersection", true, nil, true, "denied"},
		{"preview remains observational", false, nil, true, "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rc := review.NewContext(review.Request{})
			rc.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "service.go", Patch: "+change", TokenCount: 2}}}
			rc.Source.Policy = projectTrustedPolicy(TrustedPolicyResult{Enforced: tc.enforced, Effective: &policy.EffectivePolicy{AllowedTools: tc.capabilities}})
			if tc.skill {
				rc.Source.SkillActivations = []review.SkillActivation{{AllowedTools: []string{"diff-analyzer"}}}
			}
			got := (&Pipeline{}).executeReviewToolRequest(rc, review.ToolRequest{Name: "get_diff_summary"})
			if got.Status != tc.want {
				t.Fatalf("got %#v, want %s", got, tc.want)
			}
			if tc.want == "ok" && !strings.Contains(got.Result, `"file_count":1`) {
				t.Fatalf("authorized read lost its captured diff: %#v", got)
			}
			if tc.want == "denied" && got.Result != "" {
				t.Fatal("denied read exposed snapshot evidence")
			}
		})
	}
}

func TestDeniedToolsAreNotAdvertisedToModel(t *testing.T) {
	rc := review.NewContext(review.Request{})
	rc.Source.Policy = projectTrustedPolicy(TrustedPolicyResult{Enforced: true, Effective: &policy.EffectivePolicy{AllowedTools: []string{"model.review"}}})
	for _, tool := range reasonerToolSchemas() {
		if strings.Contains(reviewSystemPrompt(rc), `"name":"`+tool.Name+`"`) {
			t.Fatalf("denied native tool %s was advertised in system prompt", tool.Name)
		}
	}
}

type policyRecordingProvider struct {
	requests []llm.LLMRequest
}

func (p *policyRecordingProvider) Name() string { return "fake" }
func (p *policyRecordingProvider) Complete(_ context.Context, req llm.LLMRequest) (string, error) {
	p.requests = append(p.requests, req)
	if len(p.requests) == 1 {
		return `{"findings":[],"tool_requests":[{"name":"get_merge_request","input":{},"reason":"read private context"}]}`, nil
	}
	return `{"findings":[],"skill_coverage":[]}`, nil
}

func TestPolicyDenialSurvivesModelRequestedToolAndFollowup(t *testing.T) {
	model := &policyRecordingProvider{}
	limits := policyTestLimits()
	p := &Pipeline{Orchestrator: orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "fake"), map[string]orchestrator.LLMProvider{"fake": model}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(policyTestBudget{}))}
	rc := review.NewContext(review.Request{})
	rc.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "main.go", Patch: "+change"}}}
	rc.Source.Policy = projectTrustedPolicy(TrustedPolicyResult{Enforced: true, Effective: &policy.EffectivePolicy{AllowedTools: []string{"model.review"}}})
	bindPolicyTestBudget(&rc.Source.Policy)
	rc.Source.SkillActivations = []review.SkillActivation{{Name: "permissive", AllowedTools: []string{"scm-api"}}}
	rc.Source.SCM = &review.SCMContext{Description: "PRIVATE_TOOL_RESULT"}
	if _, _, _, _, err := p.runReview(context.Background(), "test", rc); err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 2 || len(rc.Source.ToolObservations) != 1 {
		t.Fatalf("expected initial dispatch, denial and followup: requests=%d observations=%#v", len(model.requests), rc.Source.ToolObservations)
	}
	for _, request := range model.requests {
		if len(request.Tools) != 0 {
			t.Fatalf("denied tool advertised to native provider: %#v", request.Tools)
		}
	}
	observation := rc.Source.ToolObservations[0]
	if observation.Status != "denied" || observation.Result != "" || strings.Contains(model.requests[1].UserMessage, "PRIVATE_TOOL_RESULT") {
		t.Fatalf("unauthorized tool data reached followup: %#v", observation)
	}
}
