package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func budgetTestLimits() review.ResourceLimits {
	s := review.ResourceLimitScope{ModelCalls: 20, InputTokens: 100000, OutputTokens: 10000, ActiveMS: 1000, MoneyMicro: 100000}
	return review.ResourceLimits{Currency: "USD", Attempt: s, Change: s, Project: s}
}

// Only a reservation protocol fake: no production persistence/pricing claim.
type recordingBudget struct {
	mu         sync.Mutex
	intents    []DispatchIntent
	outcomes   []DispatchOutcome
	denyAfter  int
	recordErr  error
	badReceipt bool
	replay     bool
	activeMS   int64
	notAfter   time.Time
}

type stagedBudget struct {
	recordingBudget
	stage       string
	order       []string
	prepared    DispatchIntent
	legacyCalls int
}

func (b *stagedBudget) ReserveAndBegin(context.Context, DispatchIntent) (DispatchPermit, error) {
	b.legacyCalls++
	return DispatchPermit{}, errors.New("legacy path forbidden")
}
func (b *stagedBudget) PrepareAction(_ context.Context, i DispatchIntent) (PreparedAction, error) {
	b.order = append(b.order, "prepare")
	b.prepared = i
	if b.stage == "prepare" {
		return PreparedAction{}, errors.New("prepare failed")
	}
	p := PreparedAction{ID: i.ActionID, RequestDigest: i.RequestDigest}
	if b.stage == "bad prepare" {
		p.ID = "wrong"
	}
	return p, nil
}
func (b *stagedBudget) ClaimPrepared(_ context.Context, p PreparedAction, worker string, lease time.Duration) (ActionClaim, error) {
	b.order = append(b.order, "claim")
	if b.stage == "claim" {
		return ActionClaim{}, errors.New("claim failed")
	}
	c := ActionClaim{ID: p.ID, RequestDigest: p.RequestDigest, WorkerID: worker, Fence: p.Fence + 1, LeaseUntil: time.Now().Add(lease), Intent: &b.prepared, Job: b.prepared.Job}
	if b.stage == "bad claim" {
		c.RequestDigest = "wrong"
	}
	return c, nil
}
func (b *stagedBudget) BeginPrepared(_ context.Context, c ActionClaim) (DispatchPermit, error) {
	b.order = append(b.order, "begin")
	if b.stage == "begin" {
		return DispatchPermit{}, errors.New("begin failed")
	}
	p := DispatchPermit{ID: c.ID, RequestDigest: c.RequestDigest, Fence: c.Fence, WorkerID: c.WorkerID, ActiveMS: 10, NotAfter: c.LeaseUntil}
	if b.stage == "bad begin" {
		p.Fence++
	}
	return p, nil
}

func TestFencedAuthorityStagesEveryLiveDispatchAndNeverFallsBackToLegacy(t *testing.T) {
	for _, stage := range []string{"", "prepare", "bad prepare", "claim", "bad claim", "begin", "bad begin"} {
		t.Run(stage, func(t *testing.T) {
			limits := budgetTestLimits()
			a := &stagedBudget{stage: stage}
			calls := 0
			provider := budgetProvider{call: func(context.Context, LLMRequest) (string, error) {
				calls++
				if strings.Join(a.order, ",") != "prepare,claim,begin" {
					t.Error("provider preceded fenced sending")
				}
				return "[]", nil
			}}
			o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": provider}, WithOperatorCeilings(&limits), WithBudgetAuthority(a))
			_, err := o.Complete(context.Background(), fundedContext(limits), RoleReasoner, "test", "sys", "user")
			if stage == "" {
				if err != nil || calls != 1 || len(a.outcomes) != 1 {
					t.Fatalf("staged dispatch failed: calls=%d err=%v", calls, err)
				}
			} else if err == nil || !IsBudgetError(err) || calls != 0 || len(a.outcomes) != 0 {
				t.Fatalf("failed stage authorized I/O: stage=%s calls=%d err=%v", stage, calls, err)
			}
			if a.legacyCalls != 0 {
				t.Fatal("fenced authority downgraded to immediate-send API")
			}
		})
	}
}

func (b *recordingBudget) ReserveAndBegin(_ context.Context, i DispatchIntent) (DispatchPermit, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.denyAfter > 0 && len(b.intents) >= b.denyAfter {
		return DispatchPermit{}, errors.New("shared capacity exhausted")
	}
	b.intents = append(b.intents, i)
	p := DispatchPermit{ID: fmt.Sprint(len(b.intents)), RequestDigest: i.RequestDigest, AlreadyStarted: b.replay, ActiveMS: b.activeMS, NotAfter: b.notAfter}
	if b.badReceipt {
		p.RequestDigest = "wrong"
	}
	return p, nil
}
func (b *recordingBudget) RecordOutcome(ctx context.Context, _ DispatchPermit, o DispatchOutcome) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	b.outcomes = append(b.outcomes, o)
	return b.recordErr
}

type budgetProvider struct {
	call func(context.Context, LLMRequest) (string, error)
}

func (p budgetProvider) Name() string { return "fake" }
func (p budgetProvider) Complete(ctx context.Context, r LLMRequest) (string, error) {
	return p.call(ctx, r)
}

func fundedContext(limits review.ResourceLimits) *review.Context {
	rc := review.NewContext(review.Request{})
	rc.Source.Policy = review.PolicyProjection{Mode: "enforce", Digest: "test-policy", Limits: &limits, OperatorCeilings: &limits, AllowedCapabilities: []string{"model.review"}}
	return rc
}

func TestDispatchRetainsHostJobClaimOutsideModelInput(t *testing.T) {
	limits := budgetTestLimits()
	authority := &recordingBudget{}
	o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": budgetProvider{call: func(context.Context, LLMRequest) (string, error) { return "[]", nil }}}, WithOperatorCeilings(&limits), WithBudgetAuthority(authority))
	claim := review.JobClaim{RunID: "run-test", Token: "private-owner"}
	ctx := review.WithJobClaim(context.Background(), claim)
	if _, err := o.Complete(ctx, fundedContext(limits), RoleReasoner, "test", "sys", "user"); err != nil {
		t.Fatal(err)
	}
	if len(authority.intents) != 1 || authority.intents[0].Job == nil || *authority.intents[0].Job != claim {
		t.Fatalf("host claim was lost: %#v", authority.intents)
	}
	if strings.Contains(string(authority.intents[0].Payload), claim.Token) {
		t.Fatal("ownership token leaked into model payload")
	}
}

func TestBudgetCeilingsDenyBeforeReservationAndIO(t *testing.T) {
	for _, name := range []string{"zero attempt", "zero change", "zero project", "output request", "stale authority", "missing limits", "bad receipt", "legacy with host caps", "preview with host caps"} {
		t.Run(name, func(t *testing.T) {
			limits := budgetTestLimits()
			rc := fundedContext(limits)
			authority := &recordingBudget{}
			switch name {
			case "zero attempt":
				limits.Attempt.ModelCalls = 0
			case "zero change":
				limits.Change.InputTokens = 0
			case "zero project":
				limits.Project.ActiveMS = 0
			case "output request":
				limits.Attempt.OutputTokens = 1
			case "stale authority":
				rc.Source.Policy.OperatorCeilings = &review.ResourceLimits{}
			case "missing limits":
				rc.Source.Policy.Limits = nil
			case "bad receipt":
				authority.badReceipt = true
			case "legacy with host caps":
				rc.Source.Policy = review.PolicyProjection{}
				authority = nil
			case "preview with host caps":
				rc.Source.Policy = review.PolicyProjection{Mode: "preview"}
				authority = nil
			}
			if strings.HasPrefix(name, "zero") || name == "output request" {
				rc = fundedContext(limits)
			}
			opts := []Option{WithOperatorCeilings(&limits)}
			if authority != nil {
				opts = append(opts, WithBudgetAuthority(authority))
			}
			o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": forbiddenPolicyProvider{t}}, opts...)
			_, err := o.Complete(context.Background(), rc, RoleReasoner, "test", "sys", "user")
			if err == nil || !strings.Contains(err.Error(), "budget") {
				t.Fatalf("budget bypass: %v", err)
			}
			if authority != nil && name != "bad receipt" && len(authority.intents) != 0 {
				t.Fatal("invalid request reached authority")
			}
		})
	}
}

func TestEveryFallbackReservesActualPayloadAndUnknownUsageRetainsHold(t *testing.T) {
	limits := budgetTestLimits()
	authority := &recordingBudget{}
	cfg := DefaultOrchestratorConfig("review", "small", "fake")
	role := cfg.Roles[RoleReasoner]
	role.Fallbacks = []ModelSpec{{Provider: "backup", Model: "fallback-model"}}
	cfg.Roles[RoleReasoner] = role
	calls := 0
	provider := budgetProvider{call: func(_ context.Context, req LLMRequest) (string, error) {
		calls++
		if len(authority.intents) != calls {
			t.Fatal("provider preceded reservation")
		}
		if calls == 1 {
			return "", errors.New("uncertain transport")
		}
		return "answer", nil
	}}
	o := NewOrchestrator(cfg, map[string]LLMProvider{"fake": provider, "backup": provider}, WithOperatorCeilings(&limits), WithBudgetAuthority(authority))
	result, err := o.Complete(context.Background(), fundedContext(limits), RoleReasoner, "repair_findings", "sys", "user")
	if err != nil || result != "answer" || calls != 2 || len(authority.outcomes) != 2 {
		t.Fatalf("fallback protocol: calls=%d err=%v", calls, err)
	}
	if authority.intents[1].Provider != "backup" || authority.intents[1].Model != "fallback-model" || authority.intents[0].RequestDigest == authority.intents[1].RequestDigest || !strings.Contains(string(authority.intents[1].Payload), "user") {
		t.Fatal("reservation not bound to actual payload/route")
	}
	if authority.outcomes[0].ResponseReceived || !authority.outcomes[1].ResponseReceived || authority.outcomes[0].UsageKnown || authority.outcomes[1].UsageKnown {
		t.Fatal("unknown usage incorrectly settled")
	}
}

func TestBudgetFailureNeverFallsBackOrReplays(t *testing.T) {
	for _, recordFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(recordFailure), func(t *testing.T) {
			limits := budgetTestLimits()
			authority := &recordingBudget{denyAfter: 1}
			if recordFailure {
				authority.recordErr = errors.New("journal unavailable")
			}
			cfg := DefaultOrchestratorConfig("review", "small", "fake")
			role := cfg.Roles[RoleReasoner]
			role.Fallbacks = []ModelSpec{{Provider: "fake", Model: "fallback"}, {Provider: "fake", Model: "third"}}
			cfg.Roles[RoleReasoner] = role
			calls := 0
			p := budgetProvider{call: func(context.Context, LLMRequest) (string, error) { calls++; return "", errors.New("uncertain") }}
			o := NewOrchestrator(cfg, map[string]LLMProvider{"fake": p}, WithOperatorCeilings(&limits), WithBudgetAuthority(authority))
			_, err := o.Complete(context.Background(), fundedContext(limits), RoleReasoner, "test", "sys", "user")
			if err == nil || !strings.Contains(err.Error(), "budget") || calls != 1 {
				t.Fatalf("budget failure replayed request: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestParallelBatchesShareReservationAuthority(t *testing.T) {
	limits := budgetTestLimits()
	authority := &recordingBudget{denyAfter: 1}
	p := budgetProvider{call: func(context.Context, LLMRequest) (string, error) { return "funded", nil }}
	o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": p}, WithOperatorCeilings(&limits), WithBudgetAuthority(authority))
	err := o.CompleteParallel(context.Background(), fundedContext(limits), "sys", [][]review.FileDiff{{{Path: "a"}}, {{Path: "b"}}}, func([]review.FileDiff) string { return "user" })
	if err == nil || len(authority.intents) != 1 || len(authority.outcomes) != 1 {
		t.Fatalf("parallel overspend: intents=%d outcomes=%d err=%v", len(authority.intents), len(authority.outcomes), err)
	}
}

func TestCancellationRecordsUnknownOutcomeWithoutFallback(t *testing.T) {
	limits := budgetTestLimits()
	authority := &recordingBudget{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := budgetProvider{call: func(context.Context, LLMRequest) (string, error) { cancel(); return "", context.Canceled }}
	o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": p}, WithOperatorCeilings(&limits), WithBudgetAuthority(authority))
	_, err := o.Complete(ctx, fundedContext(limits), RoleReasoner, "test", "sys", "user")
	if !errors.Is(err, context.Canceled) || len(authority.outcomes) != 1 || authority.outcomes[0].UsageKnown {
		t.Fatalf("cancellation dropped outcome: %v %#v", err, authority.outcomes)
	}
}

func TestOperatorCeilingSnapshotsAreDetached(t *testing.T) {
	limits := budgetTestLimits()
	limits.Change.Period = &review.FixedBudgetPeriod{Kind: "fixed", Anchor: time.Now(), DurationMS: 1000}
	o := NewOrchestrator(nil, nil, WithOperatorCeilings(&limits))
	limits.Change.Period.DurationMS = 0
	copy := o.OperatorCeilings()
	copy.Change.Period.DurationMS = 0
	if o.OperatorCeilings().Change.Period.DurationMS != 1000 {
		t.Fatal("caller mutated authority ceilings")
	}
}

func TestDurableSendingReplayNeverCallsProviderAgain(t *testing.T) {
	l := budgetTestLimits()
	authority := &recordingBudget{replay: true}
	o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": forbiddenPolicyProvider{t}}, WithOperatorCeilings(&l), WithBudgetAuthority(authority))
	_, err := o.Complete(context.Background(), fundedContext(l), RoleReasoner, "test", "system", "user")
	if err == nil || !strings.Contains(err.Error(), "resending") || len(authority.outcomes) != 0 {
		t.Fatalf("sending replay authorized I/O: %v", err)
	}
	if authority.intents[0].ActionID == "" || !strings.HasPrefix(authority.intents[0].RequestDigest, "sha256:") {
		t.Fatal("missing canonical action/request identity")
	}
}

func TestReservedActiveTimeBoundIsEnforcedByDispatch(t *testing.T) {
	l := budgetTestLimits()
	authority := &recordingBudget{activeMS: 5}
	provider := budgetProvider{call: func(ctx context.Context, _ LLMRequest) (string, error) { <-ctx.Done(); return "", ctx.Err() }}
	o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": provider}, WithOperatorCeilings(&l), WithBudgetAuthority(authority))
	_, err := o.Complete(context.Background(), fundedContext(l), RoleReasoner, "test", "system", "user")
	if !errors.Is(err, context.DeadlineExceeded) || len(authority.outcomes) != 1 {
		t.Fatalf("reserved active bound lost: %v", err)
	}
}

func TestExpiredReservationCannotSendUnderOldPriceAuthority(t *testing.T) {
	l := budgetTestLimits()
	authority := &recordingBudget{notAfter: time.Now().Add(-time.Second)}
	o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": forbiddenPolicyProvider{t}}, WithOperatorCeilings(&l), WithBudgetAuthority(authority))
	_, err := o.Complete(context.Background(), fundedContext(l), RoleReasoner, "test", "system", "user")
	if err == nil || len(authority.outcomes) != 1 || authority.outcomes[0].ResponseReceived {
		t.Fatalf("expired reservation reached I/O: %v %#v", err, authority.outcomes)
	}
}

func TestStreamingPathsReserveBeforeEmitting(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprint(native), func(t *testing.T) {
			limits := budgetTestLimits()
			authority := &recordingBudget{}
			var provider LLMProvider = budgetProvider{call: func(context.Context, LLMRequest) (string, error) { return "compatibility", nil }}
			if native {
				provider = streamingProvider{}
			}
			o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": provider}, WithOperatorCeilings(&limits), WithBudgetAuthority(authority))
			emitted := false
			_, err := o.StreamComplete(context.Background(), fundedContext(limits), RoleFormatter, "operator_reply", "sys", "user", func(string) error {
				emitted = true
				if len(authority.intents) != 1 {
					t.Fatal("emission preceded reservation")
				}
				return nil
			})
			if err != nil || !emitted || len(authority.outcomes) != 1 || !authority.outcomes[0].ResponseReceived {
				t.Fatalf("unaccounted stream: err=%v emitted=%v outcomes=%#v", err, emitted, authority.outcomes)
			}
		})
	}
}

func TestActiveTimeCeilingCancelsProviderAndRecordsOutcome(t *testing.T) {
	limits := budgetTestLimits()
	limits.Attempt.ActiveMS = 5
	authority := &recordingBudget{}
	p := budgetProvider{call: func(ctx context.Context, _ LLMRequest) (string, error) { <-ctx.Done(); return "", ctx.Err() }}
	o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": p}, WithOperatorCeilings(&limits), WithBudgetAuthority(authority))
	_, err := o.Complete(context.Background(), fundedContext(limits), RoleReasoner, "test", "sys", "user")
	if !errors.Is(err, context.DeadlineExceeded) || len(authority.outcomes) != 1 {
		t.Fatalf("active bound not enforced: err=%v outcomes=%#v", err, authority.outcomes)
	}
}

func TestEnforcedDispatchCannotSpendWithoutBudgetAuthority(t *testing.T) {
	for _, mode := range []string{"complete", "stream", "parallel"} {
		t.Run(mode, func(t *testing.T) {
			o := NewOrchestrator(DefaultOrchestratorConfig("review", "small", "fake"), map[string]LLMProvider{"fake": forbiddenPolicyProvider{t}})
			rc := review.NewContext(review.Request{})
			rc.Source.Policy = review.PolicyProjection{Enforced: true, AllowedCapabilities: []string{"model.review"}}
			var err error
			switch mode {
			case "complete":
				_, err = o.Complete(context.Background(), rc, RoleReasoner, "unfunded", "system", "user")
			case "stream":
				_, err = o.StreamComplete(context.Background(), rc, RoleFormatter, "unfunded", "system", "user", func(string) error { t.Error("unfunded output emitted"); return nil })
			case "parallel":
				err = o.CompleteParallel(context.Background(), rc, "system", [][]review.FileDiff{{{Path: "a.go"}}, {{Path: "b.go"}}}, func([]review.FileDiff) string { return "user" })
			}
			if err == nil || !strings.Contains(err.Error(), "budget") {
				t.Fatalf("missing budget authority did not deny dispatch: %v", err)
			}
		})
	}
}
