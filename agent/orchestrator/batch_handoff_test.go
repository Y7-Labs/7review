package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type atomicBatchBudget struct {
	recordingBudget
	staged []DispatchIntent
	fault  string
}

func (b *atomicBatchBudget) PrepareModelBatch(_ context.Context, intents []DispatchIntent) ([]PreparedAction, error) {
	b.staged = append([]DispatchIntent(nil), intents...)
	if b.fault == "prepare" {
		return nil, errors.New("atomic commit failed")
	}
	var prepared []PreparedAction
	for _, i := range intents {
		prepared = append(prepared, PreparedAction{ID: i.ActionID, RequestDigest: i.RequestDigest})
	}
	switch b.fault {
	case "receipt_count":
		prepared = prepared[:1]
	case "receipt_id":
		prepared[1].ID = "foreign"
	case "receipt_digest":
		prepared[1].RequestDigest = "foreign"
	case "receipt_started":
		prepared[1].AlreadyStarted = true
	case "receipt_fence":
		prepared[1].Fence = 1
	}
	return prepared, nil
}

func batchHandoffContext(t *testing.T, limits review.ResourceLimits) *review.Context {
	t.Helper()
	now := time.Now().UTC()
	req := review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", SourceSHA: "head", TargetSHA: "base"}
	scm := &review.SCMContext{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DiffRefs: review.DiffRefs{BaseSHA: "base", HeadSHA: "head"}, Files: []review.ChangedFile{{NewPath: "a.go", Patch: "+one", Status: "modified"}, {NewPath: "b.go", Patch: "+two", Status: "modified"}}}
	_, att, err := review.AttestSCMRequest(req, scm, now)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	attempt := review.AttemptIdentity{AttemptID: "batch-attempt", WorkIntentID: "batch-work", IntentKind: review.IntentManual, Change: review.ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "org/repo", ChangeNumber: "7"}, Snapshot: att.Snapshot, PolicyDigest: digest, Generation: 1, State: review.AttemptInvestigating, Version: 1, CreatedAt: now, UpdatedAt: now}
	execution := review.ExecutionContext{Mode: review.ExecutionService, ProducerID: "unit-test", Persistence: review.PersistenceDurableService, TrustedBoundaryID: "unit-test", ComparisonTree: att.ComparisonTree, DeadlineAt: now.Add(time.Minute)}
	rc, err := review.NewVerifiedContext(req, attempt, execution, att)
	if err != nil {
		t.Fatal(err)
	}
	rc.Source.SCM = scm
	rc.Source.Policy = review.PolicyProjection{Mode: "enforce", Enforced: true, Digest: digest, Limits: &limits, OperatorCeilings: &limits, AllowedCapabilities: []string{"model.review"}}
	rc.Source.Run.ID = "batch-run"
	rc.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "a.go", Patch: "+one", TokenCount: 1}, {Path: "b.go", Patch: "+two", TokenCount: 1}}}
	return rc
}

func TestCanonicalBatchPlanningNeverFallsBackToIndividualDispatch(t *testing.T) {
	for _, fault := range []string{"", "serial", "prepare", "receipt_count", "receipt_id", "receipt_digest", "receipt_started", "receipt_fence", "authority", "missing_claim", "partition", "source", "builder_mutation", "partition_builder_mutation", "validator", "cancelled", "changed_routing"} {
		t.Run(fault, func(t *testing.T) {
			limits := budgetTestLimits()
			a := &atomicBatchBudget{fault: fault}
			calls := 0
			p := budgetProvider{call: func(context.Context, LLMRequest) (string, error) { calls++; return "[]", nil }}
			cfg := DefaultOrchestratorConfig("review", "small", "fake")
			for name, role := range cfg.Roles {
				role.MaxTokens = 1024
				if fault == "serial" {
					role.Parallel = false
				}
				cfg.Roles[name] = role
			}
			o := NewOrchestrator(cfg, map[string]LLMProvider{"fake": p}, WithOperatorCeilings(&limits), WithBudgetAuthority(a))
			rc := batchHandoffContext(t, limits)
			ctx := context.Background()
			if fault != "missing_claim" {
				ctx = review.WithJobClaim(ctx, review.JobClaim{RunID: rc.Source.Run.ID, Token: "private-live-owner"})
			}
			routing, err := o.RoutingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			validation := &review.ModelValidation{Version: 1, MinConfidence: 0.45}
			if fault == "validator" {
				validation = nil
			}
			ctx, err = review.WithModelCheckpoint(review.WithModelRouting(review.WithModelValidation(ctx, validation), routing), rc, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 2})
			if err != nil {
				t.Fatal(err)
			}
			batches := [][]review.FileDiff{{rc.Source.Diff.Files[0]}, {rc.Source.Diff.Files[1]}}
			switch fault {
			case "authority":
				o.budgetAuthority = &a.recordingBudget
			case "partition":
				batches[1][0].Path = "other.go"
			case "source":
				rc.Source.Run.Warnings = append(rc.Source.Run.Warnings, "changed after checkpoint")
			case "changed_routing":
				role := cfg.Roles[RoleReasoner]
				role.Primary.Model = "changed"
				role.MaxTokens = 999
				cfg.Roles[RoleReasoner] = role
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			tool := ToolDefinition{Name: "bounded", InputSchema: map[string]any{"maximum": json.Number("9007199254740993")}}
			err = o.CompleteParallel(ctx, rc, "system", batches, func(batch []review.FileDiff) string {
				if fault == "builder_mutation" {
					rc.Source.Run.Warnings = append(rc.Source.Run.Warnings, "mutated by builder")
				}
				if fault == "partition_builder_mutation" {
					batch[0].Patch = "+wrong after partition check"
				}
				return batch[0].Path
			}, tool)
			if fault == "" || fault == "serial" || fault == "changed_routing" {
				if !errors.Is(err, ErrDurableContinuation) || len(a.staged) != 2 {
					t.Fatalf("batch did not hand off atomically: %#v %v", a.staged, err)
				}
				for index, i := range a.staged {
					var req LLMRequest
					decoder := json.NewDecoder(bytes.NewReader(i.Payload))
					decoder.UseNumber()
					if err = decoder.Decode(&req); err != nil {
						t.Fatal(err)
					}
					if len(req.Tools) != 1 || req.Tools[0].InputSchema["maximum"] != json.Number("9007199254740993") {
						t.Fatal("batch cloning changed the exact tool schema")
					}
					cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
					if err != nil || cp.Cursor.BatchIndex != index || cp.Cursor.BatchCount != 2 || req.Model != "review" || req.MaxTokens != 1024 || i.Job == nil || i.Job.Token != "private-live-owner" || strings.Contains(string(i.Payload), i.Job.Token) {
						t.Fatalf("batch was rerouted, unbound or leaked ownership: %#v %v", i, err)
					}
				}
			} else if err == nil || !IsBudgetError(err) || errors.Is(err, ErrDurableContinuation) {
				t.Fatalf("invalid batch gained handoff: %v", err)
			}
			if calls != 0 || len(a.intents) != 0 {
				t.Fatalf("batch fell back to individual effects: calls=%d legacy=%d", calls, len(a.intents))
			}
		})
	}
}
