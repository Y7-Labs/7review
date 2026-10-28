package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

const referencedRule = `{"schema_version":1,"id":"AUTH-001","title":"Authorization boundary","statement":"BASE_RULE_SENTINEL: authorize before privileged mutation.","checks":["auth-before-write"]}`

func ruleReferenceFixture(t *testing.T, document string) (SCMPolicyAdmission, *methodFileReader) {
	t.Helper()
	a, reader := packReferenceFixture(t, referencedPack)
	var pack map[string]any
	if err := json.Unmarshal([]byte(referencedPack), &pack); err != nil {
		t.Fatal(err)
	}
	pack["pack"].(map[string]any)["rule_refs"] = []string{".7review/rules/auth.json"}
	data, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	reader.files[".7review/packs/security.json"] = string(data)
	if document != "" {
		reader.files[".7review/rules/auth.json"] = document
	}
	return a, reader
}

func TestTrustedRuleEvidenceCannotBeReplacedByHeadCorpus(t *testing.T) {
	rc := review.NewContext(review.Request{})
	rc.Source.Policy.Enforced = true
	rc.Source.Policy.SourceRevision = "base"
	rc.Source.Policy.Documents = []review.PolicyDocument{{Kind: "rule", Path: ".7review/rules/active.md"}, {Kind: "rule", Path: ".7review/rules/inactive.md"}}
	rc.CorpusSections = []review.Section{{Path: ".7review/rules/active.md", Content: "HEAD_ACTIVE_INJECTION"}, {Path: ".7review/rules/inactive.md", Content: "HEAD_INACTIVE_INJECTION"}, {Path: "README.md", Content: "unrelated context"}}
	rc.Source.Evidence = []review.EvidenceItem{{Source: ".7review/rules/active.md", AuthorityLevel: "sot"}, {Source: ".7review/rules/inactive.md", AuthorityLevel: "sot"}}
	applyTrustedRuleEvidence(rc, []policy.ResolvedRule{{RuleV1: policy.RuleV1{ID: "AUTH-001", Title: "Auth", Statement: "BASE_ONLY_RULE"}, Source: policy.DocumentSource{Path: ".7review/rules/active.md"}}})
	prompt := reviewSystemPrompt(rc)
	if strings.Contains(prompt, "HEAD_ACTIVE_INJECTION") || strings.Contains(prompt, "HEAD_INACTIVE_INJECTION") || !strings.Contains(prompt, "BASE_ONLY_RULE") || !strings.Contains(prompt, "unrelated context") {
		t.Fatal("head corpus overrode base rule authority or unrelated evidence was discarded")
	}
	if len(rc.Source.Evidence) != 1 || rc.Source.Evidence[0].HeadingOrKey != "AUTH-001" {
		t.Fatalf("head rule authority survived: %#v", rc.Source.Evidence)
	}
}

func TestRuleCitationMapsToStableGateRuleIdentity(t *testing.T) {
	source := review.Source{
		Policy:         review.PolicyProjection{Rules: []review.PolicyDocument{{ID: "AUTH-001", Path: ".7review/rules/auth.json"}}},
		ChangedFiles:   []review.ChangedFile{{NewPath: "auth.go"}},
		Findings:       []review.Finding{{ID: "finding", Location: review.Location{Path: "auth.go"}, ValidationStatus: "accepted", Citations: []review.EvidenceCitation{{Source: ".7review/rules/auth.json", HeadingOrKey: "AUTH-001", Rule: "Authorize before mutation.", Violation: "Mutates before authorization."}}}},
		CorpusSections: []review.Section{{Path: ".7review/rules/auth.json", Title: "AUTH-001", Content: "Authorize before mutation."}},
		Evidence:       []review.EvidenceItem{{Source: ".7review/rules/auth.json", HeadingOrKey: "AUTH-001", AuthorityLevel: "sot"}},
	}
	findings := reviewGateFindings(source)
	if len(findings) != 1 || findings[0].RuleID != "AUTH-001" {
		t.Fatalf("text quote replaced stable gate rule identity: %#v", findings)
	}
	source.Findings[0].Citations = append([]review.EvidenceCitation{{Source: "supporting.md", Rule: "unrelated quote"}}, source.Findings[0].Citations...)
	findings = reviewGateFindings(source)
	if findings[0].RuleID != "AUTH-001" {
		t.Fatalf("valid later citation was ignored by the gate: %#v", findings)
	}
	source.Findings[0].Citations = []review.EvidenceCitation{{Source: ".7review/rules/auth.json", HeadingOrKey: "AUTH-001", Rule: "not in the selected statement", Violation: "claimed violation"}}
	if citedPolicyRuleID(source, source.Findings[0].Citations) != "" {
		t.Fatal("stable ID was accepted without a verifiable base quotation")
	}
}

func TestInheritedRuleStatementAndChecksReachExecution(t *testing.T) {
	req, scm := trustedPolicyFixture()
	a, _ := ruleReferenceFixture(t, referencedRule)
	model := &methodPromptProvider{}
	store := NewMemoryRunStore()
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = a
	if err := p.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) == 0 || !strings.Contains(model.prompts[0], "BASE_RULE_SENTINEL") || !strings.Contains(model.prompts[0], "auth-before-write") {
		t.Fatalf("inherited rule did not reach investigation: %q", model.prompts)
	}
	run, err := store.Get(context.Background(), "owner/repository!7")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range run.Source.Evidence {
		if e.Source == ".7review/rules/auth.json" && e.HeadingOrKey == "AUTH-001" && e.AuthorityLevel == "sot" && e.CanJustifyFinding {
			found = true
		}
	}
	if !found {
		t.Fatalf("base rule lacks authoritative citation evidence: %#v", run.Source.Evidence)
	}
}

func TestUnmatchedRuleIsValidatedButNotInjectedIntoExecution(t *testing.T) {
	req, scm := trustedPolicyFixture()
	scm.Files[0].NewPath = "frontend/view.go"
	scm.Files[0].OldPath = "frontend/view.go"
	a, reader := ruleReferenceFixture(t, referencedRule)
	model := &methodPromptProvider{}
	p := policyExecutionPipeline(NewMemoryRunStore(), model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = a
	if err := p.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) == 0 || strings.Contains(model.prompts[0], "BASE_RULE_SENTINEL") {
		t.Fatal("unmatched rule became global authority")
	}
	if !strings.Contains(strings.Join(reader.reads, ","), ".7review/rules/auth.json") {
		t.Fatal("unmatched reference was not validated")
	}
}

func TestInvalidRuleStopsBeforeInferenceEvenWhenPackDoesNotMatch(t *testing.T) {
	req, scm := trustedPolicyFixture()
	scm.Files[0].NewPath = "frontend/view.go"
	scm.Files[0].OldPath = "frontend/view.go"
	a, _ := ruleReferenceFixture(t, "")
	model := &methodPromptProvider{}
	p := policyExecutionPipeline(NewMemoryRunStore(), model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = a
	err := p.Run(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), ".7review/rules/auth.json") || len(model.prompts) != 0 {
		t.Fatalf("missing unmatched rule silently ignored: err=%v calls=%d", err, len(model.prompts))
	}
}

func TestRulePreviewRemainsObservational(t *testing.T) {
	req, scm := trustedPolicyFixture()
	a, _ := ruleReferenceFixture(t, referencedRule)
	a.Mode = "preview"
	store := NewMemoryRunStore()
	model := &methodPromptProvider{}
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = a
	if err := p.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	run, err := store.Get(context.Background(), "owner/repository!7")
	if err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) == 0 || strings.Contains(model.prompts[0], "BASE_RULE_SENTINEL") || len(run.Source.SkillActivations) != 0 || len(run.Source.Policy.Rules) != 1 || run.Source.Policy.Enforced {
		t.Fatalf("rule preview changed execution or lost inspection state: policy=%#v", run.Source.Policy)
	}
	a, _ = ruleReferenceFixture(t, "")
	a.Mode = "preview"
	result, err := a.Evaluate(context.Background(), req, scm)
	if err != nil || result.Enforced || !result.Trigger.Accepted || !strings.Contains(result.Warning, ".7review/rules/auth.json") {
		t.Fatalf("preview missing-rule warning was enforced or hidden: %#v err=%v", result, err)
	}
}
