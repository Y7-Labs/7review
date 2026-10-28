package ci

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func TestCIWithoutReservationAuthorityIsIncompleteAndNeverCallsProvider(t *testing.T) {
	repo, base, head := ciGitFixture(t)
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "panic"), map[string]orchestrator.LLMProvider{"panic": panicProvider{}})
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	result, err := Run(context.Background(), Options{RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true, Environment: Environment{Provider: "github", Host: "github.com", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"}, DeadlineAt: now.Add(time.Minute), Now: func() time.Time { return now }, Orchestrator: orch})
	if err != nil {
		t.Fatal(err)
	}
	if ExitCode(result) != 2 || result.Gate.Outcome != review.GateIncomplete || !strings.Contains(result.Assessment.StopReason, "budget") {
		t.Fatalf("unfunded CI reported success: gate=%#v assessment=%#v", result.Gate, result.Assessment)
	}
	if result.Source.Policy.Limits == nil {
		t.Fatal("CI artifact lost admitted limits")
	}
}

// Test-only funding for method/rule fixtures; not a ledger or price qualifier.
type ciTestBudget struct{}

func (ciTestBudget) ReserveAndBegin(_ context.Context, i orchestrator.DispatchIntent) (orchestrator.DispatchPermit, error) {
	return orchestrator.DispatchPermit{ID: "test-only", RequestDigest: i.RequestDigest}, nil
}
func (ciTestBudget) RecordOutcome(context.Context, orchestrator.DispatchPermit, orchestrator.DispatchOutcome) error {
	return nil
}

func fundedCIOrchestrator(provider string, p orchestrator.LLMProvider, authorities ...orchestrator.BudgetAuthority) *orchestrator.Orchestrator {
	scope := review.ResourceLimitScope{ModelCalls: 10, ToolCalls: 20, InputTokens: 1000, OutputTokens: 200, MoneyMicro: 100, ActiveMS: 1000, ObservationBytes: 4096}
	period := &review.FixedBudgetPeriod{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 86400000}
	limits := review.ResourceLimits{Currency: "USD", Attempt: scope, Change: scope, Project: scope}
	limits.Change.Period = period
	limits.Project.Period = period
	cfg := orchestrator.DefaultOrchestratorConfig("review", "small", provider)
	for name, role := range cfg.Roles {
		role.MaxTokens = 200
		cfg.Roles[name] = role
	}
	var authority orchestrator.BudgetAuthority = ciTestBudget{}
	if len(authorities) > 0 {
		authority = authorities[0]
	}
	return orchestrator.NewOrchestrator(cfg, map[string]orchestrator.LLMProvider{provider: p}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(authority))
}

type ciOneCallBudget struct{ calls int }

func (b *ciOneCallBudget) ReserveAndBegin(_ context.Context, i orchestrator.DispatchIntent) (orchestrator.DispatchPermit, error) {
	b.calls++
	if b.calls > 1 {
		return orchestrator.DispatchPermit{}, errors.New("shared budget exhausted")
	}
	return orchestrator.DispatchPermit{ID: "first", RequestDigest: i.RequestDigest}, nil
}
func (*ciOneCallBudget) RecordOutcome(context.Context, orchestrator.DispatchPermit, orchestrator.DispatchOutcome) error {
	return nil
}

func TestCIDeniedRepairRetainsPartialModelAudit(t *testing.T) {
	repo, base, head := ciGitFixture(t)
	authority := &ciOneCallBudget{}
	orch := fundedCIOrchestrator("fake", ciStaticProvider{response: "malformed first response"}, authority)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	result, err := Run(context.Background(), Options{RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true, Environment: Environment{Provider: "github", Host: "github.com", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"}, DeadlineAt: now.Add(time.Minute), Now: func() time.Time { return now }, Orchestrator: orch})
	if err != nil {
		t.Fatal(err)
	}
	if ExitCode(result) != 2 || authority.calls != 2 || !strings.Contains(result.Assessment.StopReason, "budget") || len(result.Source.Model.RawResponses) != 1 || result.Source.Policy.OperatorCeilings == nil {
		t.Fatalf("denied repair lost accounting/audit state: calls=%d assessment=%#v model=%#v", authority.calls, result.Assessment, result.Source.Model)
	}
}
