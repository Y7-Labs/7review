package ci

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/internal/testpostgres"
)

type canonicalLedgerProvider struct {
	calls    int
	response string
}

func (*canonicalLedgerProvider) Name() string { return "fake" }
func (p *canonicalLedgerProvider) Complete(context.Context, llm.LLMRequest) (string, error) {
	p.calls++
	return p.response, nil
}

func TestCIFrozenRunnerUsesDurableIntakeAndDoesNotReplayDuplicate(t *testing.T) {
	db := testpostgres.Open(t)
	if err := accounting.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	repo, base, head := ciGitFixture(t)
	limits := fundedCIOrchestrator("fake", panicProvider{}).OperatorCeilings()
	routes := []accounting.RouteBound{}
	for _, model := range []string{"review", "small"} {
		routes = append(routes, accounting.RouteBound{Provider: "fake", Model: model, Version: "test-fixture-v1", Currency: "USD", EvidenceRef: "test-only:controlled-provider", ValidFrom: time.Now().UTC().Add(-time.Hour), ValidUntil: time.Now().UTC().Add(time.Hour), InputHardLimit: 1000, OutputHardLimit: 200, FixedMicro: 1, MaxActiveMS: 500, AllOutputBounded: true, NativeToolsQualified: true})
	}
	catalog, err := accounting.NewCatalog(routes)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := accounting.NewAuthority(db, accounting.Config{BoundaryID: "ci-test", ProjectID: "team", Limits: *limits, Catalog: catalog})
	if err != nil {
		t.Fatal(err)
	}
	provider := &canonicalLedgerProvider{response: `{"findings":[],"skill_coverage":[{"name":"builtin/correctness","status":"covered","evidence":["diff:service.go"],"tools":[],"checks":["correctness"],"notes":"changed code inspected"}]}`}
	// This provider fixture only records calls; it does not qualify a live route.
	orch := fundedCIOrchestrator("fake", provider, authority)
	opts := Options{RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true, BoundaryID: "ci-test", Intake: authority, RuntimeCeilings: limits, Environment: Environment{Provider: "github", Host: "github.com", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7", RunAttempt: "1"}, DeadlineAt: time.Now().UTC().Add(time.Minute), Orchestrator: orch}
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || result.Source.Attempt.AttemptID == "" || result.Source.Execution.Mode != review.ExecutionEphemeral || ExitCode(result) != 0 {
		t.Fatalf("canonical CI did not execute through ledger: calls=%d assessment=%#v", provider.calls, result.Assessment)
	}
	firstID := result.Source.Attempt.AttemptID
	replay, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || replay.Source.Attempt.AttemptID != firstID || ExitCode(replay) != 2 || !strings.Contains(replay.Assessment.StopReason, "duplicate") {
		t.Fatalf("duplicate CI replayed inference or claimed completion: calls=%d assessment=%#v", provider.calls, replay.Assessment)
	}
	opts.Environment.RunAttempt = "2"
	result, err = Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || result.Source.Attempt.AttemptID == firstID || ExitCode(result) != 2 || !strings.Contains(result.Assessment.StopReason, "budget") {
		t.Fatalf("new job reset shared allowance: calls=%d assessment=%#v", provider.calls, result.Assessment)
	}
}
