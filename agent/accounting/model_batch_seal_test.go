package accounting

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestSealedModelBatchClaimAndBeginRecheckMembership(t *testing.T) {
	for _, scenario := range []struct{ stage, fault string }{{"claim", "digest"}, {"begin", "digest"}, {"claim", "missing_slot"}, {"begin", "duplicate_slot"}, {"begin", "unbound"}} {
		t.Run(scenario.stage+"/"+scenario.fault, func(t *testing.T) {
			stage := scenario.stage
			a, intents := modelBatchFixture(t)
			ctx := context.Background()
			prepared, err := a.PrepareModelBatch(ctx, intents)
			if err != nil {
				t.Fatal(err)
			}
			var claim orchestrator.ActionClaim
			if stage == "begin" {
				claim, err = a.ClaimPrepared(ctx, prepared[0], "claimed-owner", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
			}
			switch scenario.fault {
			case "digest":
				_, err = a.db.Exec(`UPDATE budget_model_batches SET manifest_digest='revoked'`)
			case "unbound":
				_, err = a.db.Exec(`UPDATE budget_jobs SET model_batch_id=NULL`)
			default:
				var raw []byte
				if err = a.db.QueryRow(`SELECT manifest FROM budget_model_batches`).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var seal modelBatchSeal
				if err = json.Unmarshal(raw, &seal); err != nil {
					t.Fatal(err)
				}
				if scenario.fault == "missing_slot" {
					seal.Slots = seal.Slots[:1]
				} else {
					seal.Slots[1] = seal.Slots[0]
				}
				raw, err = json.Marshal(seal)
				if err != nil {
					t.Fatal(err)
				}
				_, err = a.db.Exec(`UPDATE budget_model_batches SET manifest=$1,manifest_digest=$2`, raw, hash(raw))
			}
			if err != nil {
				t.Fatal(err)
			}
			if stage == "claim" {
				if c, err := a.ClaimPrepared(ctx, prepared[0], "new-owner", time.Minute); err == nil || c.Fence != 0 {
					t.Fatalf("changed membership claimed: %#v %v", c, err)
				}
			} else if p, err := a.BeginPrepared(ctx, claim); err == nil || p.ID != "" {
				t.Fatalf("changed membership sent: %#v %v", p, err)
			}
			var sending int
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_actions WHERE state<>'prepared'`).Scan(&sending); err != nil || sending != 0 {
				t.Fatalf("changed seal reached I/O: %d %v", sending, err)
			}
		})
	}
}

func TestSealedModelBatchAllowsOnlyProvenFormatterContinuation(t *testing.T) {
	a, intents := modelBatchFixture(t)
	ctx := context.Background()
	// Freeze the formatter route in the common group, as canonical execution does.
	for index := range intents {
		cp, err := review.DecodeModelCheckpoint(intents[index].ModelCheckpoint)
		if err != nil {
			t.Fatal(err)
		}
		cp.Routing = &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{"reasoner": {Routes: []review.ModelRoute{{Provider: intents[index].Provider, Model: intents[index].Model}}, MaxTokens: 20}, "formatter": {Routes: []review.ModelRoute{{Provider: intents[index].Provider, Model: intents[index].Model}}, MaxTokens: 20}}}
		intents[index].ModelCheckpoint, err = json.Marshal(cp)
		if err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prepared {
		claim, err := a.ClaimPrepared(ctx, p, "batch-owner", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		permit, err := a.BeginPrepared(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		raw := []byte("malformed findings")
		if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: raw, ResponseReceived: true, ResponseDigest: hash(raw), ElapsedMS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.RecoverActionResults(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
		t.Fatal(err)
	}
	child, err := a.AdvanceModelContinuation(ctx, prepared[0].ID, pipeline.ModelContinuationV1{})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimPrepared(ctx, child, "formatter-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("[]")
	if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: raw, ResponseReceived: true, ResponseDigest: hash(raw), ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RecoverActionResults(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
		t.Fatal(err)
	}
	if p, err := a.CompleteModelJob(ctx, child.ID); err != nil || p == nil || !p.Succeeded {
		t.Fatalf("sealed batch failed valid continuation: %#v %v", p, err)
	}
	if _, err = a.db.Exec(`UPDATE budget_model_batches SET manifest_digest='revoked'`); err != nil {
		t.Fatal(err)
	}
	if p, err := a.LookupModelJob(ctx, intents[0].Job.RunID); err == nil || p != nil {
		t.Fatalf("changed seal served terminal result: %#v %v", p, err)
	}
}
