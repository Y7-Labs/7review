package ci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/policy"
)

func TestCIOperatorCeilingsRejectRepositoryIncreaseBeforeModel(t *testing.T) {
	repo, base, head := ciGitFixture(t)
	data, err := os.ReadFile(filepath.Join(repo, ".7review/review.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := policy.Decode("review.json", data)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"attempt", "change", "project"} {
		t.Run(scope, func(t *testing.T) {
			ceilings := c.Limits.Clone()
			switch scope {
			case "attempt":
				ceilings.Attempt.ModelCalls = 0
			case "change":
				ceilings.Change.ModelCalls = 0
			case "project":
				ceilings.Project.ModelCalls = 0
			}
			orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "panic"), map[string]orchestrator.LLMProvider{"panic": panicProvider{}})
			now := time.Now().UTC()
			_, err := Run(context.Background(), Options{RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true, RuntimeCeilings: &ceilings, Environment: Environment{Provider: "github", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"}, DeadlineAt: now.Add(time.Minute), Orchestrator: orch})
			if err == nil || !strings.Contains(err.Error(), "limits."+scope+".model_calls exceeds operator ceiling") {
				t.Fatalf("operator ceilings did not reach CI admission: %v", err)
			}
		})
	}
}
