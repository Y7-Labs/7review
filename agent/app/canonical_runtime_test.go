package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/internal/testpostgres"
)

type canonicalServiceSCM struct{}

func (canonicalServiceSCM) Enrich(_ context.Context, r review.Request) (*review.SCMContext, error) {
	return &review.SCMContext{Provider: r.Provider, ProjectID: r.ProjectID, ChangeID: r.ChangeID, DiffRefs: review.DiffRefs{BaseSHA: "base", HeadSHA: "head"}, Files: []review.ChangedFile{{NewPath: "service.go", Patch: "+change", Status: "modified"}}}, nil
}

type canonicalServicePolicy struct{ limits review.ResourceLimits }

func (p canonicalServicePolicy) Evaluate(_ context.Context, r review.Request, s *review.SCMContext) (pipeline.TrustedPolicyResult, error) {
	_, att, err := review.AttestSCMRequest(r, s, time.Now().UTC())
	if err != nil {
		return pipeline.TrustedPolicyResult{}, err
	}
	return pipeline.TrustedPolicyResult{Mode: "enforce", Enforced: true, Attestation: att, Trigger: policy.TriggerDecision{Accepted: true}, Effective: &policy.EffectivePolicy{Digest: "sha256:" + strings.Repeat("b", 64), Limits: p.limits, OperatorCeilings: &p.limits, AllowedTools: []string{"repo.read", "model.review"}}}, nil
}

func TestCanonicalServiceRuntimePersistsIntakeButDeniesUnqualifiedModel(t *testing.T) {
	db, dsn := testpostgres.OpenDSN(t)
	if err := accounting.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACCOUNTING_DATABASE_URL", dsn)
	t.Setenv("ACCOUNTING_BOUNDARY_ID", "service-test")
	t.Setenv("ACCOUNTING_PROJECT_ID", "team")
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("unqualified model reached HTTP")
		http.Error(w, "forbidden", 500)
	}))
	defer model.Close()
	scope := review.ResourceLimitScope{ModelCalls: 10, ToolCalls: 10, InputTokens: 1000000, OutputTokens: 10000, MoneyMicro: 1000, ActiveMS: 10000, ObservationBytes: 1000000}
	period := &review.FixedBudgetPeriod{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 86400000}
	limits := review.ResourceLimits{Currency: "USD", Attempt: scope, Change: scope, Project: scope}
	limits.Change.Period = period
	limits.Project.Period = period
	cfg := &config.Config{Provider: "openai_compat", ProviderAPIKey: "test-key", ProviderBaseURL: model.URL, ReviewModel: "review", SmallModel: "small", PolicyV2Mode: "enforce", PolicyV2Path: ".7review/review.json", PolicyRuntimeCeilings: &limits, GitHubAPIURL: "https://api.github.com", GitHubToken: "test-scm", MemoryDir: t.TempDir(), CorpusRoot: t.TempDir(), WebhookWorkers: 1, WebhookQueueSize: 4, WebhookJobTimeout: 5000}
	s, err := newServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		close(s.work)
	})
	if s.pipeline.Canonical == nil {
		t.Fatal("configured service did not wire canonical intake")
	}
	s.pipeline.SCM = canonicalServiceSCM{}
	s.pipeline.TrustedPolicy = canonicalServicePolicy{limits}
	req := review.Request{Provider: "github", ProjectID: "org/repo", Repository: "org/repo", ChangeID: "7", SourceSHA: "head", TargetSHA: "base", WebURL: "https://untrusted.example/org/repo/pull/7"}
	queued, err := s.requestReview(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var run *pipeline.Run
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		run, err = s.pipeline.Jobs.Get(context.Background(), queued.RunID)
		if err == nil && run.Status == pipeline.StatusFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if run == nil || run.Source == nil || run.Status != pipeline.StatusFailed || run.Source.Attempt.AttemptID == "" || run.Source.Attempt.Change.Host != "github.com" || !strings.Contains(run.Error, "qualified") {
		t.Fatalf("queued run did not retain canonical failed intake: queued=%#v run=%#v err=%v", queued, run, err)
	}
	var attempts, actions int
	_ = db.QueryRow(`SELECT count(*) FROM budget_attempts`).Scan(&attempts)
	_ = db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions)
	if attempts != 1 || actions != 0 || calls.Load() != 0 {
		t.Fatalf("unqualified intake spent or was not durable: attempts=%d actions=%d calls=%d", attempts, actions, calls.Load())
	}
	var linkedAttempt string
	if err = db.QueryRow(`SELECT attempt_id FROM budget_jobs WHERE run_id=$1`, queued.RunID).Scan(&linkedAttempt); err != nil || linkedAttempt != run.Source.Attempt.AttemptID {
		t.Fatalf("live worker did not link its job to canonical attempt: %s %v", linkedAttempt, err)
	}
	// Canonical delivery validation must not be bypassed by process-local dedup.
	req.EventAction = "opened"
	req.DeliveryID = "same-delivery"
	req.PayloadDigest = canonicalPayloadDigest([]byte("body-one"))
	first, err := s.enqueueReview(req, true)
	if err != nil {
		t.Fatal(err)
	}
	waitStatus := func(id string, want pipeline.RunStatus) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			r, e := s.pipeline.Jobs.Get(context.Background(), id)
			if e == nil && r.Status == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("run %s did not reach %s", id, want)
	}
	waitStatus(first.RunID, pipeline.StatusIgnored)
	req.PayloadDigest = canonicalPayloadDigest([]byte("body-two"))
	_, err = s.enqueueReview(req, true)
	if err == nil {
		t.Fatal("conflicting replay accepted before worker intake")
	}
	var conflicts int
	if err = db.QueryRow(`SELECT count(*) FROM budget_inbox_conflicts`).Scan(&conflicts); err != nil || conflicts != 1 || calls.Load() != 0 {
		t.Fatalf("process-local dedup hid payload conflict: conflicts=%d calls=%d err=%v", conflicts, calls.Load(), err)
	}
}
