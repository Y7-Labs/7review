package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

func unmatchedMethodFixture(t *testing.T, id, body string) (SCMPolicyAdmission, *methodFileReader) {
	t.Helper()
	a, reader := methodAdmissionFixture(t, "builtin/correctness", body)
	c, err := policy.Decode("review.yaml", []byte(reader.files[".7review/review.yaml"]))
	if err != nil {
		t.Fatal(err)
	}
	c.Packs[0].Match = policy.MatchV2{Paths: []string{"unmatched/**"}}
	c.Packs[0].Methods = []string{id}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	reader.files[".7review/review.yaml"] = string(data)
	return a, reader
}

func TestUnmatchedInvalidMethodCannotCrossModelBoundary(t *testing.T) {
	for _, tc := range []struct{ name, id, body string }{
		{"missing", "repo/backend", ""},
		{"malformed", "repo/backend", "not a method"},
		{"unknown builtin", "builtin/unknown", ""},
		{"traversal", "repo/../../escape", ""},
		{"duplicate checks", "repo/backend", strings.Replace(backendMethod, "required-checks: backend-contract", "required-checks: backend-contract backend-contract", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, scm := trustedPolicyFixture()
			a, _ := unmatchedMethodFixture(t, tc.id, tc.body)
			model := &methodPromptProvider{}
			p := policyExecutionPipeline(NewMemoryRunStore(), model, scm, TrustedPolicyResult{})
			p.TrustedPolicy = a
			err := p.Run(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tc.id) || len(model.prompts) != 0 {
				t.Fatalf("unmatched invalid method was ignored: err=%v calls=%d", err, len(model.prompts))
			}
		})
	}
}

func TestUnmatchedMethodIsValidatedButNotActivated(t *testing.T) {
	req, scm := trustedPolicyFixture()
	a, reader := unmatchedMethodFixture(t, "repo/backend", backendMethod)
	model := &methodPromptProvider{}
	store := NewMemoryRunStore()
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = a
	if err := p.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(reader.reads, ","), ".7review/methods/backend/SKILL.md") {
		t.Fatal("unmatched method was never validated")
	}
	if len(model.prompts) == 0 || strings.Contains(model.prompts[0], "BASE_METHOD_SENTINEL") || strings.Contains(model.prompts[0], "backend-contract") {
		t.Fatal("unmatched method became execution guidance or mandatory coverage")
	}
	run, err := store.Get(context.Background(), "owner/repository!7")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Source.Policy.ResolvedMethods) != 1 || run.Source.Policy.ResolvedMethods[0].ID != "builtin/correctness" {
		t.Fatalf("unmatched method became active: %#v", run.Source.Policy)
	}
	if len(run.Source.Policy.MethodRegistry) != 2 {
		t.Fatalf("validated inactive method not retained as provenance: %#v", run.Source.Policy.MethodRegistry)
	}
}

func TestMethodRegistryBoundsCancellationAndSharedReferences(t *testing.T) {
	ids := make([]string, maxDeclaredPolicyMethods+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("repo/m%d", i)
	}
	e := policy.EffectivePolicy{Methods: []string{"builtin/correctness"}}
	if _, _, err := resolvePolicyMethodRegistry(context.Background(), &e, policy.Source{}, ids, func(context.Context, string) ([]byte, error) {
		t.Fatal("oversized registry reached reader")
		return nil, nil
	}); err == nil {
		t.Fatal("oversized registry accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := resolvePolicyMethodRegistry(ctx, &e, policy.Source{}, nil, nil); err != context.Canceled {
		t.Fatalf("cancelled builtin registry continued: %v", err)
	}
	e = policy.EffectivePolicy{Methods: []string{"repo/backend"}}
	reads := 0
	_, registry, err := resolvePolicyMethodRegistry(context.Background(), &e, policy.Source{Revision: "base"}, []string{"repo/backend", "repo/backend"}, func(context.Context, string) ([]byte, error) { reads++; return []byte(backendMethod), nil })
	if err != nil || reads != 1 || len(registry) != 1 {
		t.Fatalf("shared method fetched repeatedly: reads=%d registry=%v err=%v", reads, registry, err)
	}
}

func TestInactiveMethodContentAffectsIdentityButCannotEnterHeadCorpus(t *testing.T) {
	req, scm := trustedPolicyFixture()
	a, reader := unmatchedMethodFixture(t, "repo/backend", backendMethod)
	first, err := a.Evaluate(context.Background(), req, scm)
	if err != nil {
		t.Fatal(err)
	}
	reader.files[".7review/methods/backend/SKILL.md"] = backendMethod + "\nExtra check guidance.\n"
	second, err := a.Evaluate(context.Background(), req, scm)
	if err != nil {
		t.Fatal(err)
	}
	if first.Effective.Digest == second.Effective.Digest {
		t.Fatal("inactive declared method content absent from identity")
	}
	rc := review.NewContext(req)
	rc.Source.Policy = projectTrustedPolicy(first)
	rc.CorpusSections = []review.Section{{Path: ".7review/methods/backend/SKILL.md", Title: "head", Content: "HEAD_INACTIVE_METHOD"}}
	applyTrustedRuleEvidence(rc, nil)
	if len(rc.CorpusSections) != 0 {
		t.Fatal("inactive method gained authority through head corpus retrieval")
	}
}

func TestUnmatchedMethodFailureIsVisibleButNotEnforcedInPreview(t *testing.T) {
	req, scm := trustedPolicyFixture()
	a, _ := unmatchedMethodFixture(t, "repo/backend", "")
	a.Mode = "preview"
	result, err := a.Evaluate(context.Background(), req, scm)
	if err != nil || result.Enforced || !result.Trigger.Accepted || !strings.Contains(result.Warning, "repo/backend") {
		t.Fatalf("unmatched method failure hidden in preview: %#v err=%v", result, err)
	}
}
