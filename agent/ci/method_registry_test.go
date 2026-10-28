package ci

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/policy"
)

func TestFrozenRunnerRejectsUnmatchedMissingMethodBeforeModel(t *testing.T) {
	repo, base, _ := ciGitFixture(t)
	ciGitRun(t, repo, "checkout", "-b", "unmatched-method", base)
	data, err := os.ReadFile(filepath.Join(repo, ".7review/review.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config policy.ReviewConfigV2
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config.Packs = []policy.MethodPackV2{{ID: "inactive", Match: policy.MatchV2{Paths: []string{"unmatched/**"}}, Methods: []string{"repo/backend"}, Checks: []string{}, RiskFloor: "none", Publication: policy.PublicationConstraintV2{Mode: "inherit", ArtifactClasses: []string{}}}}
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	ciWriteFile(t, repo, ".7review/review.json", string(data))
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "declare missing inactive method")
	base = strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
	ciWriteFile(t, repo, "service.go", "package service\nfunc Added() {}\n")
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "unrelated change")
	head := strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
	now := time.Now().UTC()
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "panic"), map[string]orchestrator.LLMProvider{"panic": panicProvider{}})
	_, err = Run(context.Background(), Options{RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true, Environment: Environment{Provider: "github", Host: "github.com", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"}, DeadlineAt: now.Add(time.Minute), Orchestrator: orch})
	if err == nil || !strings.Contains(err.Error(), "repo/backend") {
		t.Fatalf("inactive missing method crossed CI admission: %v", err)
	}
}

func TestFrozenRunnerRejectsBaseMethodSymlinkEvenWhenHeadIsRegular(t *testing.T) {
	repo, base, _ := ciGitFixture(t)
	ciGitRun(t, repo, "checkout", "-b", "base-method-link", base)
	data, err := os.ReadFile(filepath.Join(repo, ".7review/review.json"))
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
	methodPath := filepath.Join(repo, ".7review/methods/backend/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(methodPath), 0700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: backend\ndescription: This is symlink data, not an authorized regular method.\nrequired-checks: correctness\n---\nNever activate symlink text as policy.\n"
	if err := os.Symlink(body, methodPath); err != nil {
		t.Fatal(err)
	}
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "base method symlink")
	base = strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
	if err := os.Remove(methodPath); err != nil {
		t.Fatal(err)
	}
	ciWriteFile(t, repo, ".7review/methods/backend/SKILL.md", body)
	ciWriteFile(t, repo, "service.go", "package service\nfunc Added() {}\n")
	ciGitRun(t, repo, "add", ".")
	ciGitRun(t, repo, "commit", "-m", "head regular method")
	head := strings.TrimSpace(ciGitRun(t, repo, "rev-parse", "HEAD"))
	now := time.Now().UTC()
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "panic"), map[string]orchestrator.LLMProvider{"panic": panicProvider{}})
	_, err = Run(context.Background(), Options{RepositoryDir: repo, PolicyPath: ".7review/review.json", Offline: true, Environment: Environment{Provider: "github", Host: "github.com", RepositoryID: "org/repo", BaseRevision: base, HeadRevision: head, PipelineID: "42", JobID: "review", ChangeID: "7"}, DeadlineAt: now.Add(time.Minute), Orchestrator: orch})
	if err == nil || !strings.Contains(err.Error(), "regular blob") {
		t.Fatalf("base symlink method crossed frozen admission: %v", err)
	}
}
