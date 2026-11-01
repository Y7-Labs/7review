package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func TestServicePreparationHasNoRunAdmissionOrModelEffects(t *testing.T) {
	store := NewMemoryRunStore()
	p, req, spy, model := canonicalPipeline(t, store)
	deadline := time.Now().UTC().Add(time.Minute)
	prepared, err := p.PrepareService(context.Background(), req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	runs, _ := store.List(context.Background())
	if len(runs) != 0 || len(spy.received) != 0 || model.calls != 0 {
		t.Fatalf("preparation committed work or dispatched: runs=%d admissions=%d calls=%d", len(runs), len(spy.received), model.calls)
	}
	if prepared.Source.Attestation == nil || prepared.Admission.ExpectedGeneration != 7 || prepared.Source.Policy.Digest == "" || prepared.Admission.Change.Host != "trusted.example:443" {
		t.Fatalf("incomplete prepared authority: %#v", prepared)
	}
}

type forbiddenAcceptedSCM struct{ t *testing.T }

func (s forbiddenAcceptedSCM) Enrich(context.Context, review.Request) (*review.SCMContext, error) {
	s.t.Fatal("accepted worker re-read mutable SCM")
	return nil, nil
}

func TestAcceptedWorkerUsesFrozenPreparationWithoutReadmission(t *testing.T) {
	store := NewMemoryRunStore()
	p, req, spy, model := canonicalPipeline(t, store)
	prepared, err := p.PrepareService(context.Background(), req, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := spy.Admit(context.Background(), prepared.Admission)
	if err != nil {
		t.Fatal(err)
	}
	source := prepared.Source
	source.Attempt = receipt.Attempt
	source.Execution = receipt.Execution
	source.Attestation = &receipt.Attestation
	source.Run.ID = "run-accepted"
	aggregate := review.AcceptedAggregate{Version: 1, OriginalRequest: req, Receipt: receipt, Source: source, Continuation: prepared.Continuation}
	p.SCM = forbiddenAcceptedSCM{t}
	p.TrustedPolicy = nil
	p.Canonical.RequireJob = true
	ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: "run-accepted", Token: "owner"})
	if err = p.RunAccepted(ctx, aggregate); err != nil {
		t.Fatal(err)
	}
	if len(spy.received) != 1 || model.calls != 1 {
		t.Fatalf("accepted execution re-admitted or skipped review: admissions=%d calls=%d", len(spy.received), model.calls)
	}
}

func TestAcceptedWorkerCannotResumeIntoPublicationMode(t *testing.T) {
	store := NewMemoryRunStore()
	p, req, spy, model := canonicalPipeline(t, store)
	prepared, err := p.PrepareService(context.Background(), req, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := spy.Admit(context.Background(), prepared.Admission)
	if err != nil {
		t.Fatal(err)
	}
	source := prepared.Source
	source.Attempt = receipt.Attempt
	source.Execution = receipt.Execution
	source.Attestation = &receipt.Attestation
	source.Run.ID = "run-accepted"
	p.DeliveryMode = DeliveryPublish
	err = p.RunAccepted(review.WithJobClaim(context.Background(), review.JobClaim{RunID: source.Run.ID, Token: "owner"}), review.AcceptedAggregate{Version: 1, OriginalRequest: req, Receipt: receipt, Source: source, Continuation: prepared.Continuation})
	if err == nil || model.calls != 0 {
		t.Fatalf("accepted work resumed with ungoverned publication: calls=%d err=%v", model.calls, err)
	}
}

func TestServicePreparationRespectsTriggerAndRejectsPreview(t *testing.T) {
	for _, mode := range []string{"ignored", "preview"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryRunStore()
			p, req, spy, model := canonicalPipeline(t, store)
			admission := p.TrustedPolicy.(staticAdmission)
			if mode == "ignored" {
				admission.result.Trigger.Accepted = false
				admission.result.Trigger.Reasons = []string{"excluded"}
			} else {
				admission.result.Enforced = false
				admission.result.Mode = "preview"
			}
			p.TrustedPolicy = admission
			prepared, err := p.PrepareService(context.Background(), req, time.Now().UTC().Add(time.Minute))
			if mode == "ignored" && (err != nil || !prepared.Ignored) {
				t.Fatalf("ignored trigger became accepted work: %#v %v", prepared, err)
			}
			if mode == "preview" && err == nil {
				t.Fatal("preview policy became service authority")
			}
			runs, _ := store.List(context.Background())
			if len(runs) != 0 || len(spy.received) != 0 || model.calls != 0 {
				t.Fatal("non-acceptance created work")
			}
		})
	}
}
