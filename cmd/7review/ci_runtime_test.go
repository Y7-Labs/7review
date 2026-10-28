package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/internal/testpostgres"
)

func TestCICommandWiresCanonicalRuntimeWithoutGrantingModelRoute(t *testing.T) {
	db, dsn := testpostgres.OpenDSN(t)
	if err := accounting.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	file := writePolicyFixture(t)
	repo := filepath.Dir(file)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var cfg policy.ReviewConfigV2
	if err = json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Capabilities.Allowed = []string{"repo.read", "model.review"}
	cfg.Limits.Currency = "USD"
	cfg.Limits.Attempt.InputTokens = 100000
	cfg.Limits.Attempt.OutputTokens = 10000
	cfg.Limits.Attempt.MoneyMicro = 1000
	cfg.Limits.Change.InputTokens = 100000
	cfg.Limits.Change.OutputTokens = 10000
	cfg.Limits.Change.MoneyMicro = 1000
	cfg.Limits.Project.InputTokens = 100000
	cfg.Limits.Project.OutputTokens = 10000
	cfg.Limits.Project.MoneyMicro = 1000
	raw, _ = json.Marshal(cfg)
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = repo
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "7review test")
	git("config", "user.email", "test@example.com")
	if err = os.WriteFile(filepath.Join(repo, "service.go"), []byte("package service\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	if err = os.WriteFile(filepath.Join(repo, "service.go"), []byte("package service\nfunc Added() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "head")
	head := git("rev-parse", "HEAD")
	ceilings, _ := json.Marshal(struct {
		SchemaVersion int                   `json:"schema_version"`
		Limits        review.ResourceLimits `json:"limits"`
	}{1, cfg.Limits})
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected inference", 500) }))
	defer model.Close()
	for key, value := range map[string]string{"GITHUB_ACTIONS": "false", "GITLAB_CI": "false", "ACCOUNTING_DATABASE_URL": dsn, "ACCOUNTING_BOUNDARY_ID": "cli-test", "ACCOUNTING_PROJECT_ID": "team", "POLICY_RUNTIME_CEILINGS_JSON": string(ceilings), "PROVIDER": "openai_compat", "PROVIDER_API_KEY": "test-key", "PROVIDER_BASE_URL": model.URL, "REVIEW_MODEL": "review", "SMALL_MODEL": "small", "ORCHESTRATOR_CONFIG": ""} {
		t.Setenv(key, value)
	}
	var out bytes.Buffer
	code, err := runCICommand([]string{"review", "--repo-dir", repo, "--policy-path", "review.json", "--offline", "--provider", "github", "--provider-host", "github.com", "--repository", "org/repo", "--base", base, "--head", head, "--pipeline-id", "42", "--job-id", "review", "--change-id", "7", "--output", t.TempDir()}, &out)
	if err != nil || code != 2 || calls.Load() != 0 {
		t.Fatalf("CLI accounting boundary failed: code=%d calls=%d err=%v output=%s", code, calls.Load(), err, out.String())
	}
	var receiptJSON []byte
	if err = db.QueryRow(`SELECT receipt FROM budget_intakes`).Scan(&receiptJSON); err != nil {
		t.Fatal(err)
	}
	var receipt review.IntakeReceipt
	if err = json.Unmarshal(receiptJSON, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Execution.JobID != "review" || receipt.Execution.Mode != review.ExecutionEphemeral || receipt.Attempt.Change.Host != "github.com" {
		t.Fatalf("CLI did not bind canonical CI identity: %#v", receipt)
	}
}
