package ci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func TestScenario_S45_CIRunnerOutcomesAndDeadline(t *testing.T) {
	for _, test := range []struct {
		mode    review.GateMode
		outcome review.GateOutcome
		want    int
	}{{review.GateBlocking, review.GatePass, 0}, {review.GateBlocking, review.GateViolations, 1}, {review.GateAdvisory, review.GateViolations, 0}, {review.GateBlocking, review.GateIncomplete, 2}, {review.GateAdvisory, review.GateError, 2}} {
		if got := ExitCode(Result{Gate: review.GateResult{Mode: test.mode, Outcome: test.outcome}}); got != test.want {
			t.Fatalf("exit mapping %s/%s: got %d want %d", test.mode, test.outcome, got, test.want)
		}
	}

	repo, base, head := ciGitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	provider := cancellingProvider{cancel: cancel}
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "cancel"), map[string]orchestrator.LLMProvider{"cancel": provider})
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	result, err := Run(ctx, Options{
		RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true,
		Environment: Environment{Provider: "github", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"},
		DeadlineAt:  now.Add(time.Minute), Now: func() time.Time { return now }, Orchestrator: orch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Gate.Outcome != review.GateIncomplete || ExitCode(result) != 2 || !strings.Contains(result.Assessment.StopReason, "context canceled") {
		t.Fatalf("cancelled review must export an incomplete result: %#v", result)
	}
}

func TestScenario_S49_CIGateCycle(t *testing.T) {
	cycle := map[string][]string{"review": {"quality"}, "quality": {"review"}}
	if err := ValidateDependencyGraph("review", cycle); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("dependency cycle must be rejected: %v", err)
	}
	missing := map[string][]string{"review": {"quality"}}
	if err := ValidateDependencyGraph("review", missing); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unverifiable dependency must be rejected: %v", err)
	}
	independent := map[string][]string{"review": {"quality"}, "quality": {}}
	if err := ValidateDependencyGraph("review", independent); err != nil {
		t.Fatalf("explicit independent prerequisite should pass: %v", err)
	}
}

func TestScenario_S52_CIMergeTreeIdentity(t *testing.T) {
	repo, base, head := ciGitFixture(t)
	ciGitRun(t, repo, "checkout", "-b", "target", base)
	ciWriteFile(t, repo, "target.go", "package service\n")
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "target")
	ciGitRun(t, repo, "merge", "--no-ff", "--no-edit", head)
	merge := strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	result, err := Run(context.Background(), Options{
		RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true,
		Environment: Environment{Provider: "github", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, MergeRevision: merge, PipelineID: "42", JobID: "review", ChangeID: "7", UntrustedFork: true},
		DeadlineAt:  now.Add(time.Minute), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.HeadRevision != head || result.Snapshot.SyntheticMergeRevision != merge || result.Execution.ComparisonTree != merge || result.Snapshot.SourceToMergeMappingDigest == "" {
		t.Fatalf("synthetic merge identity was conflated with source head: %#v", result)
	}
	output := filepath.Join(t.TempDir(), "out")
	if err := Export(result, output); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(output, "run-manifest.json"))
	if err != nil || !strings.Contains(string(manifest), merge) || !strings.Contains(string(manifest), head) {
		t.Fatalf("manifest did not retain head and merge identity: %v\n%s", err, manifest)
	}

	_, err = Run(context.Background(), Options{
		RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true,
		Environment: Environment{Provider: "github", RepositoryID: "org/repo", BaseRevision: strings.Repeat("f", 40), HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7", UntrustedFork: true},
		DeadlineAt:  now.Add(time.Minute), Now: func() time.Time { return now },
	})
	if err == nil || !strings.Contains(err.Error(), "offline mode") {
		t.Fatalf("offline runner must fail when the shallow clone lacks base: %v", err)
	}
}

type cancellingProvider struct{ cancel context.CancelFunc }

func (c cancellingProvider) Name() string { return "cancel" }
func (c cancellingProvider) Complete(context.Context, llm.LLMRequest) (string, error) {
	c.cancel()
	return "", context.Canceled
}
