package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

func TestScenario_S48_CIForkIsolation(t *testing.T) {
	repo, base, head := ciGitFixture(t)
	result := runPartialFixture(t, repo, base, head)
	if result.Assessment.Completeness != review.AssessmentPartial || result.Gate.Outcome != review.GateIncomplete || ExitCode(result) != 2 {
		t.Fatalf("fork without model access must be incomplete: assessment=%#v gate=%#v", result.Assessment, result.Gate)
	}
	if len(result.Source.Findings) != 0 || !strings.Contains(result.Assessment.StopReason, "untrusted fork") {
		t.Fatalf("fork runner attempted a model review or lost its reason: %#v", result)
	}
	if result.Attestation.Snapshot.HeadRevision != head || result.Execution.Mode != review.ExecutionEphemeral {
		t.Fatalf("runner lost frozen execution identity: %#v", result)
	}
}

func runPartialFixture(t *testing.T, repo, base, head string) Result {
	t.Helper()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	orch := orchestrator.NewOrchestrator(
		orchestrator.DefaultOrchestratorConfig("review", "small", "panic"),
		map[string]orchestrator.LLMProvider{"panic": panicProvider{}},
	)
	result, err := Run(context.Background(), Options{
		RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true,
		Environment: Environment{Provider: "github", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7", UntrustedFork: true},
		DeadlineAt:  now.Add(time.Minute), Now: func() time.Time { return now }, Orchestrator: orch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRunCompletesArtifactOnlyReviewWithPolicyCoverage(t *testing.T) {
	repo, base, head := ciGitFixture(t)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		method       string
		outcome      review.GateOutcome
		completeness review.AssessmentCompleteness
		exit         int
	}{
		{"selected method", "builtin/correctness", review.GatePass, review.AssessmentComplete, 0},
		{"obsolete synthetic method cannot satisfy coverage", "effective-policy", review.GateIncomplete, review.AssessmentPartial, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := fmt.Sprintf(`{"findings":[],"skill_coverage":[{"name":%q,"status":"covered","evidence":["diff:service.go"],"tools":[],"checks":["correctness"],"notes":"changed code inspected"}]}`, tc.method)
			orch := fundedCIOrchestrator("fake", ciStaticProvider{response: response})
			result, err := Run(context.Background(), Options{
				RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true,
				Environment: Environment{Provider: "github", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"},
				DeadlineAt:  now.Add(time.Minute), Now: func() time.Time { return now }, Orchestrator: orch,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Assessment.Completeness != tc.completeness || result.Gate.Outcome != tc.outcome || ExitCode(result) != tc.exit {
				t.Fatalf("method-specific coverage mismatch: assessment=%#v coverage=%#v gate=%#v", result.Assessment, result.Coverage, result.Gate)
			}
			if strings.TrimSpace(result.Report) == "" || result.Source.Report.Draft == "" {
				t.Fatal("complete runner did not retain its draft report")
			}
			if len(result.Source.Policy.ResolvedMethods) != 1 || result.Source.Policy.ResolvedMethods[0].ID != "builtin/correctness" {
				t.Fatalf("runner lost resolved method provenance: %#v", result.Source.Policy)
			}
		})
	}
}

func ciGitFixture(t *testing.T) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	ciGitRun(t, repo, "init", "-q")
	ciGitRun(t, repo, "config", "user.name", "7review test")
	ciGitRun(t, repo, "config", "user.email", "test@example.com")
	period := &policy.FixedPeriodV2{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 86_400_000}
	limit := policy.LimitScopeV2{ModelCalls: 10, ToolCalls: 20, InputTokens: 1000, OutputTokens: 200, MoneyMicro: 100, ActiveMS: 1000, ObservationBytes: 4096}
	change, project := limit, limit
	change.Period, project.Period = period, period
	config := policy.ReviewConfigV2{
		SchemaVersion: 2, ProjectID: "org/repo",
		Defaults:     policy.DefaultsV2{Methods: []string{"builtin/correctness"}, Publication: "human_authorized", GateMode: "blocking", RequiredChecks: []string{"correctness"}},
		Limits:       policy.LimitsV2{Currency: "USD", Attempt: limit, Change: change, Project: project},
		Triggers:     policy.TriggersV2{Enabled: true, OnUpdates: true, IncludeBranches: []string{}, ExcludeBranches: []string{}, IncludeAuthors: []string{}, ExcludeAuthors: []string{}, IncludeLabels: []string{}, ExcludeLabels: []string{}},
		Capabilities: policy.CapabilitiesV2{Allowed: []string{"repo.read", "model.review"}, Required: []string{"repo.read"}},
		Domains:      map[string][]string{}, Modules: map[string][]string{}, Features: map[string][]string{},
		Delegations: []policy.DelegationV2{}, Packs: []policy.MethodPackV2{},
		QualityGate: policy.QualityGateV2{RequiredCoverage: []string{"correctness"}, MinSeverity: "high", MinStrength: "confirmed", BaselineMode: "all", ContextName: "7review/quality"},
		Retention:   policy.RetentionV2{AuditDays: 365, SourceDays: 30, UnresolvedReservationDays: 365},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	ciWriteFile(t, repo, ".7review/review.json", string(data))
	ciWriteFile(t, repo, "service.go", "package service\n")
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
	ciWriteFile(t, repo, "service.go", "package service\n\nfunc Added() {}\n")
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "head")
	return repo, base, strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
}

func ciWriteFile(t *testing.T, repo, name, content string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ciGitRun(t *testing.T, repo string, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

type ciStaticProvider struct{ response string }

func (ciStaticProvider) Name() string { return "fake" }
func (p ciStaticProvider) Complete(context.Context, llm.LLMRequest) (string, error) {
	return p.response, nil
}

type panicProvider struct{}

func (panicProvider) Name() string { return "panic" }
func (panicProvider) Complete(context.Context, llm.LLMRequest) (string, error) {
	panic("untrusted fork invoked a model provider")
}

type baseMethodProvider struct{ t *testing.T }

func (baseMethodProvider) Name() string { return "fake" }
func (p baseMethodProvider) Complete(_ context.Context, req llm.LLMRequest) (string, error) {
	if !strings.Contains(req.SystemPrompt, "TRUSTED_BASE_METHOD") || strings.Contains(req.SystemPrompt, "HEAD_METHOD_INJECTION") {
		p.t.Error("frozen runner used head method content or omitted base method")
	}
	return `{"findings":[],"skill_coverage":[{"name":"repo/backend","status":"covered","evidence":["diff:service.go"],"checks":["correctness"],"notes":"checked selected method"}]}`, nil
}

func TestFrozenRunnerExecutesBaseMethodDespiteHeadReplacement(t *testing.T) {
	repo, base, _ := ciGitFixture(t)
	ciGitRun(t, repo, "checkout", "-b", "method-base", base)
	path := filepath.Join(repo, ".7review/review.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config policy.ReviewConfigV2
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config.Defaults.Methods = []string{"repo/backend"}
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	ciWriteFile(t, repo, ".7review/review.json", string(data))
	method := "---\nname: backend\ndescription: Verify backend behavior.\nallowed-tools: diff-analyzer\nrequired-checks: correctness\n---\nTRUSTED_BASE_METHOD\n"
	ciWriteFile(t, repo, ".7review/methods/backend/SKILL.md", method)
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "trusted method")
	base = strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
	ciWriteFile(t, repo, ".7review/methods/backend/SKILL.md", strings.Replace(method, "TRUSTED_BASE_METHOD", "HEAD_METHOD_INJECTION", 1))
	ciWriteFile(t, repo, ".7review/skills/hostile/SKILL.md", "malformed proposed legacy skill must not veto trusted method execution")
	ciWriteFile(t, repo, "service.go", "package service\nfunc Added() {}\n")
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "proposed method replacement")
	head := strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	orch := fundedCIOrchestrator("fake", baseMethodProvider{t})
	result, err := Run(context.Background(), Options{
		RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true,
		Environment: Environment{Provider: "github", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"},
		DeadlineAt:  now.Add(time.Minute), Now: func() time.Time { return now }, Orchestrator: orch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Outcome != review.GatePass || len(result.Source.Policy.ResolvedMethods) != 1 || result.Source.Policy.ResolvedMethods[0].Revision != base {
		t.Fatalf("base-method execution or provenance lost: gate=%#v policy=%#v", result.Gate, result.Source.Policy)
	}
}

func TestCISkillsPathCannotOverrideTrustedMethodSelection(t *testing.T) {
	for _, path := range []string{"../escape", "/tmp/methods", ".7review/other-skills"} {
		_, err := Run(context.Background(), Options{RepositoryDir: t.TempDir(), PolicyPath: ".7review/review.yaml", DeadlineAt: time.Now().Add(time.Minute), SkillsPath: path})
		if err == nil || !strings.Contains(err.Error(), "trusted base policy") {
			t.Fatalf("legacy skills override was not rejected explicitly: path=%s err=%v", path, err)
		}
	}
}
