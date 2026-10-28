package pipeline

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/skills"
	"github.com/Y4NN777/7review/agent/tools"
)

type methodFileReader struct {
	files map[string]string
	reads []string
}

func (r *methodFileReader) ReadRepositoryFile(_ context.Context, provider, repository, revision, path string) ([]byte, error) {
	if provider != "github" || repository != "owner/repository" || revision != "base-sha" {
		return nil, fmt.Errorf("untrusted method source: %s/%s@%s", provider, repository, revision)
	}
	r.reads = append(r.reads, path)
	data, ok := r.files[path]
	if !ok {
		return nil, tools.ErrRepositoryFileNotFound
	}
	return []byte(data), nil
}

type methodPromptProvider struct{ prompts []string }

func (p *methodPromptProvider) Name() string { return "fake" }
func (p *methodPromptProvider) Complete(_ context.Context, req llm.LLMRequest) (string, error) {
	p.prompts = append(p.prompts, req.SystemPrompt)
	return `[]`, nil
}

const backendMethod = `---
name: backend
description: Check the repository backend contract.
allowed-tools: scm-api diff-analyzer
required-checks: backend-contract
---
BASE_METHOD_SENTINEL: check authorization before any privileged state mutation.
`

func methodAdmissionFixture(t *testing.T, methodID, body string) (SCMPolicyAdmission, *methodFileReader) {
	t.Helper()
	data, err := os.ReadFile("../../profiles/review.v2.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	root := strings.Replace(string(data), "[builtin/correctness]", "["+methodID+"]", 1)
	root = strings.Replace(root, "[repo/backend]", "["+methodID+"]", 1)
	reader := &methodFileReader{files: map[string]string{
		".7review/review.yaml": root,
	}}
	if body != "" {
		reader.files[".7review/methods/backend/SKILL.md"] = body
	}
	return SCMPolicyAdmission{Mode: "enforce", Path: ".7review/review.yaml", Reader: reader, RuntimeCapabilities: []string{"repo.read", "model.review"}}, reader
}

func TestScenario_S22_BaseMethodControlsExecutionNotLegacyOrHead(t *testing.T) {
	req, scm := trustedPolicyFixture()
	admission, reader := methodAdmissionFixture(t, "repo/backend", backendMethod)
	store := NewMemoryRunStore()
	model := &methodPromptProvider{}
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = admission
	p.SkillLoader = &skills.Loader{Skills: []skills.Skill{{Name: "methodology-review", Body: "LEGACY_UNSELECTED_METHOD"}}}
	if err := p.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) == 0 || !strings.Contains(model.prompts[0], "BASE_METHOD_SENTINEL") || strings.Contains(model.prompts[0], "LEGACY_UNSELECTED_METHOD") {
		t.Fatalf("selected trusted method did not control execution: prompts=%q reads=%v", model.prompts, reader.reads)
	}
	run, err := store.Get(context.Background(), "owner/repository!7")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Source.SkillSections) != 1 || run.Source.SkillSections[0].Path != ".7review/methods/backend/SKILL.md" {
		t.Fatalf("expected exactly the selected repository method: %#v", run.Source.SkillSections)
	}
	methods := run.Source.Policy.ResolvedMethods
	if len(methods) != 1 || methods[0].ID != "repo/backend" || methods[0].Revision != "base-sha" || !strings.HasPrefix(methods[0].Digest, "sha256:") {
		t.Fatalf("trusted method provenance missing: %#v", methods)
	}
}

func TestPolicyMethodFailuresStopBeforeModel(t *testing.T) {
	for _, tc := range []struct{ name, id, body string }{
		{"missing", "repo/backend", ""},
		{"malformed", "repo/backend", "not a SKILL.md method"},
		{"traversal", "repo/../../escape", backendMethod},
		{"unknown builtin", "builtin/unknown", ""},
		{"mismatched name", "repo/backend", strings.Replace(backendMethod, "name: backend", "name: correctness", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, scm := trustedPolicyFixture()
			admission, reader := methodAdmissionFixture(t, tc.id, tc.body)
			model := &methodPromptProvider{}
			p := policyExecutionPipeline(NewMemoryRunStore(), model, scm, TrustedPolicyResult{})
			p.TrustedPolicy = admission
			err := p.Run(context.Background(), req)
			if err == nil || len(model.prompts) != 0 {
				t.Fatalf("invalid method crossed model boundary: err=%v calls=%d", err, len(model.prompts))
			}
			for _, path := range reader.reads {
				if strings.Contains(path, "..") {
					t.Fatalf("unsafe method path reached SCM: %s", path)
				}
			}
		})
	}
}

func TestPreviewMethodFailureWarnsWithoutChangingLegacyExecution(t *testing.T) {
	req, scm := trustedPolicyFixture()
	admission, _ := methodAdmissionFixture(t, "repo/backend", "")
	admission.Mode = "preview"
	result, err := admission.Evaluate(context.Background(), req, scm)
	if err != nil || result.Enforced || !result.Trigger.Accepted || !strings.Contains(result.Warning, "repo/backend") {
		t.Fatalf("preview method failure was hidden or enforced: %#v err=%v", result, err)
	}
}

func TestResolvedMethodContentChangesEffectiveIdentity(t *testing.T) {
	req, scm := trustedPolicyFixture()
	admission, reader := methodAdmissionFixture(t, "repo/backend", backendMethod)
	first, err := admission.Evaluate(context.Background(), req, scm)
	if err != nil {
		t.Fatal(err)
	}
	reader.files[".7review/methods/backend/SKILL.md"] = backendMethod + "\nAdditional invariant.\n"
	second, err := admission.Evaluate(context.Background(), req, scm)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source.Digest != second.Source.Digest || first.Effective.Digest == second.Effective.Digest {
		t.Fatal("effective identity did not bind selected method content independently of root configuration")
	}
}

func TestBuiltinMethodCannotBeShadowedByRepositoryMethod(t *testing.T) {
	effective := policy.EffectivePolicy{Methods: []string{"builtin/correctness", "repo/correctness"}, Digest: "parent"}
	methods, err := resolvePolicyMethods(context.Background(), &effective, policy.Source{Revision: "base"}, func(_ context.Context, path string) ([]byte, error) {
		if path != ".7review/methods/correctness/SKILL.md" {
			t.Fatalf("unexpected repository path: %s", path)
		}
		return []byte(strings.Replace(backendMethod, "name: backend", "name: correctness", 1)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0].Activation.Name != "builtin/correctness" || methods[1].Activation.Name != "repo/correctness" || strings.Contains(methods[0].Section.Content, "BASE_METHOD_SENTINEL") {
		t.Fatalf("repository method shadowed builtin: %#v", methods)
	}
}

func TestPolicyMethodContentBoundStopsBeforeModel(t *testing.T) {
	req, scm := trustedPolicyFixture()
	admission, _ := methodAdmissionFixture(t, "repo/backend", backendMethod+strings.Repeat("x", maxPolicyMethodBytes))
	model := &methodPromptProvider{}
	p := policyExecutionPipeline(NewMemoryRunStore(), model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = admission
	if err := p.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "exceeds") || len(model.prompts) != 0 {
		t.Fatalf("oversized methods reached model: err=%v calls=%d", err, len(model.prompts))
	}
}

func TestPreviewResolvedMethodDoesNotReplaceLegacySelection(t *testing.T) {
	req, scm := trustedPolicyFixture()
	admission, _ := methodAdmissionFixture(t, "repo/backend", backendMethod)
	admission.Mode = "preview"
	model := &methodPromptProvider{}
	store := NewMemoryRunStore()
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = admission
	p.SkillLoader = &skills.Loader{Skills: []skills.Skill{{Name: "methodology-review", Body: "LEGACY_PREVIEW_METHOD"}}}
	if err := p.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) == 0 || !strings.Contains(model.prompts[0], "LEGACY_PREVIEW_METHOD") || strings.Contains(model.prompts[0], "BASE_METHOD_SENTINEL") {
		t.Fatalf("preview method changed execution guidance: %q", model.prompts)
	}
	run, err := store.Get(context.Background(), "owner/repository!7")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Source.Policy.ResolvedMethods) != 1 || run.Source.Policy.Enforced {
		t.Fatalf("preview resolution was not inspectable: %#v", run.Source.Policy)
	}
}

func TestMethodDeclaredChecksBecomeEffectiveObligations(t *testing.T) {
	req, scm := trustedPolicyFixture()
	admission, _ := methodAdmissionFixture(t, "repo/backend", strings.Replace(backendMethod, "backend-contract", "method-only-invariant", 1))
	result, err := admission.Evaluate(context.Background(), req, scm)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStringFold(result.Effective.RequiredChecks, "method-only-invariant") || !containsStringFold(result.Effective.QualityGate.RequiredCoverage, "method-only-invariant") {
		t.Fatalf("method-declared required check became optional: %#v", result.Effective)
	}
}
