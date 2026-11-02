package orchestrator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type preparedRecoveryAuthority interface {
	PreparedBudgetAuthority
	PreparedRecoveryCandidates(context.Context, int) ([]PreparedAction, error)
	ReclaimPrepared(context.Context, PreparedAction, string, time.Duration) (ActionClaim, error)
}

var errPreparedBudgetWait = errors.New("prepared action deferred within original budget")

func deferPreparedBudgetWait(ctx context.Context, a preparedRecoveryAuthority, p PreparedAction, cause error) error {
	var exhaustion interface{ BudgetExhausted() bool }
	if !errors.As(cause, &exhaustion) || !exhaustion.BudgetExhausted() {
		return cause
	}
	deferrer, ok := a.(interface {
		DeferPreparedBudgetWait(context.Context, PreparedAction) error
	})
	if !ok {
		return cause
	}
	if err := deferrer.DeferPreparedBudgetWait(ctx, p); err != nil {
		return err
	}
	return errPreparedBudgetWait
}

// RecoverPreparedActions executes bounded, immutable actions, never a pipeline or
// fallback chain. Responses are journaled only; they are not validated findings.
func (o *Orchestrator) RecoverPreparedActions(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 128 {
		return 0, errors.New("invalid prepared recovery batch bound")
	}
	if o == nil {
		return 0, errors.New("recovery orchestrator unavailable")
	}
	a, ok := o.budgetAuthority.(preparedRecoveryAuthority)
	if !ok {
		return 0, errors.New("prepared recovery authority unavailable")
	}
	scanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	candidates, err := a.PreparedRecoveryCandidates(scanCtx, limit)
	cancel()
	if err != nil {
		return 0, err
	}
	if len(candidates) > limit {
		return 0, errors.New("prepared recovery batch exceeded bound")
	}
	completed := 0
	var failures []error
	for _, p := range candidates {
		if err := ctx.Err(); err != nil {
			return completed, err
		}
		if err := o.recoverPrepared(ctx, a, p); err != nil {
			if errors.Is(err, errPreparedBudgetWait) {
				continue
			}
			failures = append(failures, fmt.Errorf("action %s: %w", p.ID, err))
		} else {
			completed++
		}
	}
	return completed, errors.Join(failures...)
}

func (o *Orchestrator) recoverPrepared(ctx context.Context, a preparedRecoveryAuthority, p PreparedAction) (err error) {
	// A panic after sending is uncertain, never an invitation to retry it.
	defer func() {
		if recover() != nil {
			err = errors.New("prepared recovery panicked; reconcile without resending")
		}
	}()
	worker, err := review.NewOpaqueID("recovery-")
	if err != nil {
		return err
	}
	c, err := a.ReclaimPrepared(ctx, p, worker, 30*time.Second)
	if err != nil {
		return deferPreparedBudgetWait(ctx, a, p, err)
	}
	if p.AlreadyStarted || p.Fence >= math.MaxInt64 || c.Fence != p.Fence+1 || c.WorkerID != worker || c.ID != p.ID || c.RequestDigest != p.RequestDigest || c.Intent == nil || c.LeaseUntil.IsZero() {
		return errors.New("invalid recovered ownership receipt")
	}
	i := c.Intent
	if err := ValidateModelCheckpoint(*i); err != nil {
		return err
	}
	if i.ActionID != p.ID || i.RequestDigest != p.RequestDigest || i.Job == nil || c.Job == nil || i.Job.RunID != c.Job.RunID || c.Job.Token == "" || i.Execution.DeadlineAt.IsZero() {
		return errors.New("recovered action attribution mismatch")
	}
	if o.operatorCeilings == nil || !reflect.DeepEqual(*o.operatorCeilings, i.OperatorCeilings) {
		return errors.New("recovered operator authority changed; readmission required")
	}
	digest := sha256.Sum256(i.Payload)
	if i.RequestDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return errors.New("recovered request digest mismatch")
	}
	var req LLMRequest
	if err := json.Unmarshal(i.Payload, &req); err != nil {
		return err
	}
	canonical, err := json.Marshal(req)
	if err != nil || !bytes.Equal(canonical, i.Payload) || req.Model != i.Model || req.MaxTokens <= 0 {
		return errors.New("recovered request is not canonical or model-bound")
	}
	active := int64(math.MaxInt64)
	limits := []review.ResourceLimits{i.OperatorCeilings}
	if i.PolicyLimits != nil {
		limits = append(limits, *i.PolicyLimits)
	}
	for _, l := range limits {
		for _, s := range []review.ResourceLimitScope{l.Attempt, l.Change, l.Project} {
			if s.ActiveMS <= 0 || s.ActiveMS > int64(math.MaxInt64/time.Millisecond) || s.ModelCalls <= 0 || s.InputTokens <= 0 || int64(req.MaxTokens) > s.OutputTokens || s.MoneyMicro < 0 {
				return errors.New("recovered request exceeds current bounds")
			}
			if s.ActiveMS < active {
				active = s.ActiveMS
			}
		}
	}
	provider, ok := o.providers[i.Provider]
	if !ok {
		return errors.New("original recovery provider unavailable")
	}
	var invoke func(context.Context) (string, error)
	switch i.Invocation {
	case "complete":
		invoke = func(ctx context.Context) (string, error) { return provider.Complete(ctx, req) }
	case "stream":
		stream, ok := provider.(StreamingLLMProvider)
		if !ok {
			return errors.New("original streaming API unavailable")
		}
		invoke = func(ctx context.Context) (string, error) {
			var b strings.Builder
			err := stream.Stream(ctx, req, func(delta string) error { b.WriteString(delta); return ctx.Err() })
			return b.String(), err
		}
	default:
		return errors.New("invocation mode absent or unsupported; no API guessing")
	}
	runCtx, cancel := context.WithDeadline(ctx, i.Execution.DeadlineAt)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return err
	}
	permit, err := a.BeginPrepared(runCtx, c)
	if err != nil {
		return deferPreparedBudgetWait(runCtx, a, p, err)
	}
	if permit.AlreadyStarted || permit.ID != p.ID || permit.RequestDigest != p.RequestDigest || permit.Fence != c.Fence || permit.WorkerID != worker || permit.NotAfter.IsZero() || permit.NotAfter.After(i.Execution.DeadlineAt) || permit.ActiveMS <= 0 || permit.ActiveMS > active {
		return errors.New("invalid recovered sending receipt; no dispatch")
	}
	_, err = o.invokeReserved(runCtx, permit, active, invoke)
	return err
}
