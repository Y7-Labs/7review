package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

type canonicalSpy struct {
	received  []review.Admission
	err       error
	duplicate bool
	mutate    func(*review.IntakeReceipt)
}

func (*canonicalSpy) ObserveGeneration(context.Context, review.ChangeKey) (uint64, error) {
	return 7, nil
}
func (s *canonicalSpy) Admit(_ context.Context, a review.Admission) (review.IntakeReceipt, error) {
	s.received = append(s.received, a)
	if s.err != nil {
		return review.IntakeReceipt{}, s.err
	}
	now := time.Now().UTC()
	id, _ := review.NewOpaqueID("attempt-")
	work, _ := review.NewOpaqueID("intent-")
	r := review.IntakeReceipt{Attempt: review.AttemptIdentity{AttemptID: id, WorkIntentID: work, IntentKind: a.IntentKind, Change: a.Change, Snapshot: a.Attestation.Snapshot, PolicyDigest: a.Policy.Digest, Generation: a.ExpectedGeneration + 1, Version: 1, State: review.AttemptAccepted, CreatedAt: now, UpdatedAt: now}, Execution: a.Execution, Attestation: a.Attestation, Policy: a.Policy, Duplicate: s.duplicate}
	if s.mutate != nil {
		s.mutate(&r)
	}
	return r, nil
}

type canonicalProvider struct {
	t     *testing.T
	store RunStore
	calls int
}

func (*canonicalProvider) Name() string { return "fake" }
func (p *canonicalProvider) Complete(ctx context.Context, _ llm.LLMRequest) (string, error) {
	p.calls++
	runs, err := p.store.List(ctx)
	if err != nil {
		p.t.Fatal(err)
	}
	for _, run := range runs {
		if run.Status == StatusRunning {
			if run.Source == nil || run.Source.Attempt.AttemptID == "" || run.Source.Execution.TrustedBoundaryID != "canonical-test" || run.Source.Attestation == nil {
				p.t.Error("provider preceded canonical checkpoint")
			}
		}
	}
	return `[]`, nil
}

func canonicalPipeline(t *testing.T, store RunStore) (*Pipeline, review.Request, *canonicalSpy, *canonicalProvider) {
	t.Helper()
	req, scm := trustedPolicyFixture()
	_, att, err := review.AttestSCMRequest(req, scm, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	spy := &canonicalSpy{}
	model := &canonicalProvider{t: t, store: store}
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{Mode: "enforce", Enforced: true, Attestation: att, Trigger: policy.TriggerDecision{Accepted: true}, Effective: &policy.EffectivePolicy{Digest: "sha256:" + strings.Repeat("b", 64), AllowedTools: []string{"repo.read", "model.review"}}})
	p.Canonical = &CanonicalIntake{Authority: spy, ProviderHosts: map[string]string{"github": "trusted.example:443"}, Execution: review.ExecutionContext{Mode: review.ExecutionService, ProducerID: "service", Persistence: review.PersistenceDurableService, TrustedBoundaryID: "canonical-test", DeadlineAt: time.Now().UTC().Add(time.Minute)}}
	return p, req, spy, model
}

func TestCanonicalPipelineCheckpointsBeforeModelAndKeepsDistinctRuns(t *testing.T) {
	store := NewMemoryRunStore()
	p, req, spy, model := canonicalPipeline(t, store)
	req.WebURL = "https://attacker.example/org/repo/pull/7"
	for n := 0; n < 2; n++ {
		if err := p.Run(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || len(spy.received) != 2 || model.calls != 2 {
		t.Fatalf("canonical runs overwritten or bypassed: runs=%d intake=%d calls=%d", len(runs), len(spy.received), model.calls)
	}
	for _, r := range runs {
		if r.Source == nil || r.Source.Attempt.AttemptID == "" || r.ID == "owner/repository!7" || r.Source.Attempt.Change.Host != "trusted.example:443" || r.Source.Attempt.Generation != 8 {
			t.Fatalf("noncanonical run identity: %#v", r)
		}
	}
}

func TestCanonicalIntakeFailureAndDuplicateNeverReachProvider(t *testing.T) {
	for _, mode := range []string{"failure", "duplicate", "publication"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryRunStore()
			p, req, spy, model := canonicalPipeline(t, store)
			switch mode {
			case "failure":
				spy.err = errors.New("intake database unavailable")
			case "duplicate":
				spy.duplicate = true
			case "publication":
				p.DeliveryMode = DeliveryPublish
			}
			err := p.Run(context.Background(), req)
			if model.calls != 0 {
				t.Fatalf("%s reached provider: %d", mode, model.calls)
			}
			if mode != "duplicate" && err == nil {
				t.Fatal("intake guard did not reject work")
			}
			if mode == "duplicate" && err != nil {
				t.Fatal(err)
			}
		})
	}
}

type failedCanonicalCheckpoint struct{ *MemoryRunStore }

func (*failedCanonicalCheckpoint) SaveContext(context.Context, string, *review.Context) error {
	return errors.New("checkpoint unavailable")
}

func TestCanonicalCheckpointFailurePreventsBudgetAndProviderIO(t *testing.T) {
	store := &failedCanonicalCheckpoint{NewMemoryRunStore()}
	p, req, _, model := canonicalPipeline(t, store)
	err := p.Run(context.Background(), req)
	if err == nil || model.calls != 0 {
		t.Fatalf("missing checkpoint reached inference: calls=%d err=%v", model.calls, err)
	}
}

var _ orchestrator.LLMProvider = (*canonicalProvider)(nil)

func TestCanonicalReceiptCannotRebindHostExecutionOrExtendDeadline(t *testing.T) {
	for _, field := range []string{"host", "boundary", "producer", "deadline"} {
		t.Run(field, func(t *testing.T) {
			store := NewMemoryRunStore()
			p, req, spy, model := canonicalPipeline(t, store)
			spy.mutate = func(r *review.IntakeReceipt) {
				switch field {
				case "host":
					r.Attempt.Change.Host = "other.example"
				case "boundary":
					r.Execution.TrustedBoundaryID = "other-boundary"
				case "producer":
					r.Execution.ProducerID = "other-producer"
				case "deadline":
					r.Execution.DeadlineAt = r.Execution.DeadlineAt.Add(time.Hour)
				}
			}
			if err := p.Run(context.Background(), req); err == nil || model.calls != 0 {
				t.Fatalf("receipt widened trusted intake: field=%s calls=%d err=%v", field, model.calls, err)
			}
		})
	}
}
