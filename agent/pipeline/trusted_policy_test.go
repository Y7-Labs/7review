package pipeline

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/tools"
)

type fakePolicyReader struct {
	data       []byte
	err        error
	provider   string
	repository string
	revision   string
	path       string
}

type staticAdmission struct {
	result TrustedPolicyResult
	err    error
}

func (a staticAdmission) Evaluate(context.Context, review.Request, *review.SCMContext) (TrustedPolicyResult, error) {
	return a.result, a.err
}

func TestPipelineEnforcedTriggerStopsBeforeModel(t *testing.T) {
	store := NewMemoryRunStore()
	model := &sequenceLLMProvider{responses: []string{`[]`}}
	p := &Pipeline{
		Orchestrator: orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "fake"), map[string]orchestrator.LLMProvider{"fake": model}),
		Jobs:         store, SCM: staticSCM{context: &review.SCMContext{
			Provider: "github", ProjectID: "org/repo", ChangeID: "7", MRIID: 7,
			DiffRefs: review.DiffRefs{BaseSHA: "base", HeadSHA: "head"},
			Files:    []review.ChangedFile{{NewPath: "main.go", Status: "modified", Patch: "+change"}},
		}},
		SCMPublisher: fakePublisher{}, Memory: NoopMemoryStore{}, ContextReducer: NoopContextReducer{},
		Policy: DefaultPolicyFilter{}, FindingValidator: DefaultFindingValidator{},
		TrustedPolicy: staticAdmission{result: TrustedPolicyResult{
			Mode: "enforce", Enforced: true,
			Trigger: policy.TriggerDecision{Accepted: false, Reasons: []string{"automatic triggers are disabled"}},
		}},
	}
	err := p.Run(context.Background(), review.Request{Provider: "github", ProjectID: "org/repo", Repository: "org/repo", ChangeID: "7", MRIID: 7})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Get(context.Background(), "org/repo!7")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != StatusIgnored || model.calls != 0 || run.Source == nil || run.Source.Policy.TriggerAccepted {
		t.Fatalf("rejected trigger crossed expensive boundary: run=%#v model_calls=%d", run, model.calls)
	}
}

func (r *fakePolicyReader) ReadRepositoryFile(_ context.Context, provider, repository, revision, path string) ([]byte, error) {
	r.provider, r.repository, r.revision, r.path = provider, repository, revision, path
	return append([]byte(nil), r.data...), r.err
}

func TestTrustedPolicyAdmissionLoadsBasePolicyAndCompilesScope(t *testing.T) {
	data, err := os.ReadFile("../../profiles/review.v2.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakePolicyReader{data: data}
	admission := SCMPolicyAdmission{
		Mode: "enforce", Path: ".7review/review.yaml", Reader: reader,
		RuntimeCapabilities: []string{"repo.read", "model.review"},
		Now:                 func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) },
	}
	req, scm := trustedPolicyFixture()
	result, err := admission.Evaluate(context.Background(), req, scm)
	if err != nil {
		t.Fatal(err)
	}
	if reader.revision != "base-sha" || reader.path != ".7review/review.yaml" {
		t.Fatalf("policy was not loaded from trusted base: %#v", reader)
	}
	if result.Effective == nil || result.Source.Revision != "base-sha" || result.Effective.RiskFloor != "medium" || strings.Join(result.Effective.MatchedPacks, ",") != "backend" {
		t.Fatalf("trusted policy not compiled for changed scope: %#v", result)
	}
	if !result.Trigger.Accepted || !result.Enforced {
		t.Fatalf("expected enforced accepted admission: %#v", result)
	}
}

func TestTrustedPolicyAdmissionPreviewFallbackAndEnforceFailure(t *testing.T) {
	req, scm := trustedPolicyFixture()
	missing := &fakePolicyReader{err: tools.ErrRepositoryFileNotFound}
	preview := SCMPolicyAdmission{Mode: "preview", Path: ".7review/review.yaml", Reader: missing, RuntimeCapabilities: []string{"repo.read"}}
	result, err := preview.Evaluate(context.Background(), req, scm)
	if err != nil || !result.Trigger.Accepted || result.Warning == "" {
		t.Fatalf("preview should disclose and continue: result=%#v err=%v", result, err)
	}
	enforce := SCMPolicyAdmission{Mode: "enforce", Path: ".7review/review.yaml", Reader: missing, RuntimeCapabilities: []string{"repo.read"}}
	if _, err := enforce.Evaluate(context.Background(), req, scm); err == nil || !errors.Is(err, tools.ErrRepositoryFileNotFound) {
		t.Fatalf("enforce must fail closed for missing policy: %v", err)
	}
	preview.Reader = &fakePolicyReader{err: errors.New("SCM unavailable")}
	result, err = preview.Evaluate(context.Background(), req, scm)
	if err != nil || !result.Trigger.Accepted || !strings.Contains(result.Warning, "could not be read") {
		t.Fatalf("preview outage must be disclosed without changing legacy admission: result=%#v err=%v", result, err)
	}
}

func TestTrustedPolicyAdmissionRejectsAutomaticTriggerWithoutChangingManualReview(t *testing.T) {
	data, err := os.ReadFile("../../profiles/review.v2.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "enabled: true", "enabled: false", 1))
	req, scm := trustedPolicyFixture()
	admission := SCMPolicyAdmission{Mode: "enforce", Path: ".7review/review.yaml", Reader: &fakePolicyReader{data: data}, RuntimeCapabilities: []string{"repo.read", "model.review"}}
	result, err := admission.Evaluate(context.Background(), req, scm)
	if err != nil {
		t.Fatal(err)
	}
	if result.Trigger.Accepted {
		t.Fatal("disabled automatic trigger must reject webhook admission")
	}
	req.DeliveryID, req.EventAction = "", "manual"
	result, err = admission.Evaluate(context.Background(), req, scm)
	if err != nil || !result.Trigger.Accepted {
		t.Fatalf("manual review must bypass automatic trigger selection: result=%#v err=%v", result, err)
	}
}

func trustedPolicyFixture() (review.Request, *review.SCMContext) {
	req := review.Request{
		Provider: "github", DeliveryID: "delivery", EventAction: "opened",
		ProjectID: "owner/repository", Repository: "owner/repository", ChangeID: "7", MRIID: 7,
		SourceSHA: "head-sha", TargetSHA: "base-sha", SourceBranch: "feature", TargetBranch: "main", Author: "alice",
		WebURL: "https://github.com/owner/repository/pull/7",
	}
	scm := &review.SCMContext{
		Provider: "github", ProjectID: "owner/repository", Repository: "owner/repository", ChangeID: "7", MRIID: 7,
		DiffRefs: review.DiffRefs{BaseSHA: "base-sha", HeadSHA: "head-sha"},
		Files:    []review.ChangedFile{{OldPath: "backend/auth.go", NewPath: "backend/auth.go", Status: "modified", Patch: "+checkAuth()"}},
	}
	return req, scm
}
