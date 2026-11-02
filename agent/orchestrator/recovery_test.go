package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type recoveryStreamProvider struct{ budgetProvider }

func (p recoveryStreamProvider) Stream(ctx context.Context, req LLMRequest, emit StreamHandler) error {
	return emit("[]")
}

func TestDispatchPersistsExactInvocationMode(t *testing.T) {
	for _, stream := range []bool{false, true} {
		limits := budgetTestLimits()
		a := &recordingBudget{}
		p := recoveryStreamProvider{budgetProvider{call: func(context.Context, LLMRequest) (string, error) { return "[]", nil }}}
		o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": p}, WithOperatorCeilings(&limits), WithBudgetAuthority(a))
		var err error
		want := "complete"
		if stream {
			want = "stream"
			_, err = o.StreamComplete(context.Background(), fundedContext(limits), RoleReasoner, "test", "sys", "user", func(string) error { return nil })
		} else {
			_, err = o.Complete(context.Background(), fundedContext(limits), RoleReasoner, "test", "sys", "user")
		}
		if err != nil || len(a.intents) != 1 {
			t.Fatalf("dispatch: %v intents=%d", err, len(a.intents))
		}
		if a.intents[0].Invocation != want {
			t.Fatalf("invocation=%q want=%q", a.intents[0].Invocation, want)
		}
	}
}

type recoveringBudget struct {
	stagedBudget
	reclaims int
}

type recoveryBudgetWaitError struct{}

func (recoveryBudgetWaitError) Error() string         { return "known capacity wait" }
func (recoveryBudgetWaitError) BudgetExhausted() bool { return true }

type waitingRecoveryBudget struct {
	recoveringBudget
	waitStage string
	deferrals int
}

func (b *waitingRecoveryBudget) ReclaimPrepared(ctx context.Context, p PreparedAction, worker string, lease time.Duration) (ActionClaim, error) {
	if b.waitStage == "reclaim" || b.waitStage == "defer_failure" || b.waitStage == "unsupported" {
		return ActionClaim{}, recoveryBudgetWaitError{}
	}
	return b.recoveringBudget.ReclaimPrepared(ctx, p, worker, lease)
}
func (b *waitingRecoveryBudget) BeginPrepared(ctx context.Context, c ActionClaim) (DispatchPermit, error) {
	if b.waitStage == "begin" {
		return DispatchPermit{}, recoveryBudgetWaitError{}
	}
	return b.recoveringBudget.BeginPrepared(ctx, c)
}
func (b *waitingRecoveryBudget) DeferPreparedBudgetWait(context.Context, PreparedAction) error {
	b.deferrals++
	if b.waitStage == "defer_failure" {
		return errors.New("scheduling store unavailable")
	}
	return nil
}

type recoveryWithoutDeferral struct{ preparedRecoveryAuthority }

func TestPreparedBudgetWaitNeverHidesPostSendingOrStorageFailures(t *testing.T) {
	for _, stage := range []string{"reclaim", "begin", "provider", "defer_failure", "unsupported"} {
		t.Run(stage, func(t *testing.T) {
			limits := budgetTestLimits()
			a := &waitingRecoveryBudget{waitStage: stage}
			req := LLMRequest{Model: "review", SystemPrompt: "stored", UserMessage: "stored", MaxTokens: 100}
			payload, _ := json.Marshal(req)
			digest := sha256.Sum256(payload)
			a.prepared = DispatchIntent{Invocation: "complete", ActionID: "prepared", RequestDigest: "sha256:" + hex.EncodeToString(digest[:]), Provider: "fake", Model: "review", Payload: payload, OperatorCeilings: limits, Execution: review.ExecutionContext{DeadlineAt: time.Now().Add(time.Minute)}, Job: &review.JobClaim{RunID: "run", Token: "old-owner"}}
			calls := 0
			provider := budgetProvider{call: func(context.Context, LLMRequest) (string, error) { calls++; return "", recoveryBudgetWaitError{} }}
			var authority BudgetAuthority = a
			if stage == "unsupported" {
				authority = recoveryWithoutDeferral{a}
			}
			o := NewOrchestrator(DefaultOrchestratorConfig("different", "small", "fake"), map[string]LLMProvider{"fake": provider}, WithOperatorCeilings(&limits), WithBudgetAuthority(authority))
			n, err := o.RecoverPreparedActions(context.Background(), 8)
			if n != 0 {
				t.Fatal("deferred or failed action was counted complete")
			}
			if stage == "reclaim" || stage == "begin" {
				if err != nil || calls != 0 || a.deferrals != 1 || len(a.outcomes) != 0 {
					t.Fatalf("known pre-send wait did not defer safely: calls=%d deferrals=%d %v", calls, a.deferrals, err)
				}
			} else if err == nil {
				t.Fatal("post-send/storage/unsupported deferral failure was hidden")
			}
			if stage == "provider" && (calls != 1 || a.deferrals != 0 || len(a.outcomes) != 1) {
				t.Fatal("provider error was treated as an unexecuted budget wait")
			}
		})
	}
}

func (b *recoveringBudget) PreparedRecoveryCandidates(context.Context, int) ([]PreparedAction, error) {
	return []PreparedAction{{ID: b.prepared.ActionID, RequestDigest: b.prepared.RequestDigest}}, nil
}

func (b *recoveringBudget) ReclaimPrepared(ctx context.Context, p PreparedAction, worker string, lease time.Duration) (ActionClaim, error) {
	b.reclaims++
	if b.stage == "reclaim" {
		return ActionClaim{}, errors.New("reclaim unavailable")
	}
	c, err := b.ClaimPrepared(ctx, p, worker, lease)
	c.Job = &review.JobClaim{RunID: b.prepared.Job.RunID, Token: "recovery-token"}
	return c, err
}

func TestPreparedRecoveryUsesOnlyOriginalRequestAndNeverFallsBack(t *testing.T) {
	for _, stage := range []string{"", "reclaim", "bad claim", "begin", "bad begin", "legacy invocation", "unknown invocation", "changed request", "provider missing", "record", "panic"} {
		t.Run(stage, func(t *testing.T) {
			limits := budgetTestLimits()
			a := &recoveringBudget{}
			req := LLMRequest{Model: "review", SystemPrompt: "stored sys", UserMessage: "stored user", MaxTokens: 100}
			payload, _ := json.Marshal(req)
			digest := sha256.Sum256(payload)
			a.prepared = DispatchIntent{Invocation: "complete", ActionID: "original-action", RequestDigest: "sha256:" + hex.EncodeToString(digest[:]), Provider: "fake", Model: "review", Payload: payload, OperatorCeilings: limits, Execution: review.ExecutionContext{DeadlineAt: time.Now().Add(time.Minute)}, Job: &review.JobClaim{RunID: "original-run", Token: "dead-owner"}}
			a.stage = stage
			switch stage {
			case "legacy invocation":
				a.prepared.Invocation = ""
			case "unknown invocation":
				a.prepared.Invocation = "guess"
			case "changed request":
				a.prepared.Payload = []byte(`{}`)
			case "provider missing":
				a.prepared.Provider = "missing"
			case "record":
				a.recordErr = errors.New("journal unavailable")
			}
			calls := 0
			p := budgetProvider{call: func(_ context.Context, got LLMRequest) (string, error) {
				calls++
				if a.order[len(a.order)-1] != "begin" || !reflect.DeepEqual(got, req) {
					t.Error("recovery changed request or preceded sending")
				}
				if stage == "panic" {
					panic("provider panic")
				}
				return "unvalidated response", nil
			}}
			o := NewOrchestrator(DefaultOrchestratorConfig("different-model", "small", "fake"), map[string]LLMProvider{"fake": p}, WithOperatorCeilings(&limits), WithBudgetAuthority(a))
			n, err := o.RecoverPreparedActions(context.Background(), 8)
			if stage == "" {
				if err != nil || n != 1 || calls != 1 || len(a.outcomes) != 1 || string(a.outcomes[0].Response) != "unvalidated response" {
					t.Fatalf("recovery: n=%d calls=%d outcomes=%d err=%v", n, calls, len(a.outcomes), err)
				}
			} else {
				wantCalls := 0
				if stage == "record" || stage == "panic" {
					wantCalls = 1
				}
				if err == nil || calls != wantCalls {
					t.Fatalf("failed open: calls=%d want=%d err=%v", calls, wantCalls, err)
				}
			}
			if a.legacyCalls != 0 {
				t.Fatal("recovery used immediate-send path")
			}
		})
	}
}
