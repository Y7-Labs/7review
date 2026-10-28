package ci

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

type canonicalCIBudget struct {
	t      *testing.T
	intent orchestrator.DispatchIntent
	calls  int
}

func (b *canonicalCIBudget) ReserveAndBegin(_ context.Context, i orchestrator.DispatchIntent) (orchestrator.DispatchPermit, error) {
	b.calls++
	b.intent = i
	if err := i.Attempt.Validate(); err != nil {
		b.t.Errorf("CI dispatched without canonical attempt: %v", err)
	}
	if i.Execution.Mode != review.ExecutionEphemeral || i.Execution.Persistence != review.PersistenceJobLocal || i.Execution.PipelineID != "42" || i.Execution.JobID != "review" || i.Attempt.Change.Host != "github.com" || i.Attestation == nil {
		b.t.Errorf("CI identity was attached after dispatch: %#v", i.Execution)
	}
	return orchestrator.DispatchPermit{}, errors.New("test authority intentionally denies spend")
}
func (*canonicalCIBudget) RecordOutcome(context.Context, orchestrator.DispatchPermit, orchestrator.DispatchOutcome) error {
	return nil
}

func TestCICanonicalIdentityExistsAtDispatchAndInFailedArtifact(t *testing.T) {
	repo, base, head := ciGitFixture(t)
	budget := &canonicalCIBudget{t: t}
	orch := fundedCIOrchestrator("panic", panicProvider{}, budget)
	now := time.Now().UTC()
	result, err := Run(context.Background(), Options{RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true, Environment: Environment{Provider: "github", Host: "github.com", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"}, DeadlineAt: now.Add(time.Minute), Orchestrator: orch})
	if err != nil {
		t.Fatal(err)
	}
	if budget.calls != 1 || result.Source.Attempt.AttemptID == "" || result.Source.Attempt.AttemptID != budget.intent.Attempt.AttemptID || result.Source.Execution != budget.intent.Execution || ExitCode(result) != 2 {
		t.Fatalf("failed CI lost canonical checkpoint: calls=%d source=%#v", budget.calls, result.Source.Attempt)
	}
}
