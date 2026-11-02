package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

// BudgetAuthority must atomically reserve attempt/change/project resources using
// trustworthy token and price bounds, and durably mark sending before returning.
// It must reject missing/invalid attempt, attestation and execution identities.
// Unknown outcomes retain their holds. There is intentionally no local default.
type BudgetAuthority interface {
	ReserveAndBegin(context.Context, DispatchIntent) (DispatchPermit, error)
	RecordOutcome(context.Context, DispatchPermit, DispatchOutcome) error
}

type DispatchIntent struct {
	Job                                                      *review.JobClaim `json:",omitempty"`
	ActionID                                                 string
	Provider, Model, Role, Step, RequestDigest, PolicyDigest string
	Payload                                                  []byte
	Attempt                                                  review.AttemptIdentity
	Execution                                                review.ExecutionContext
	Attestation                                              *review.SnapshotAttestation
	PolicyLimits                                             *review.ResourceLimits
	OperatorCeilings                                         review.ResourceLimits
}

type DispatchPermit struct {
	Fence             uint64
	WorkerID          string
	ID, RequestDigest string
	AlreadyStarted    bool
	ActiveMS          int64
	NotAfter          time.Time
}

type PreparedAction struct {
	ID, RequestDigest string
	Fence             uint64
	AlreadyStarted    bool
}

type ActionClaim struct {
	ID, RequestDigest, WorkerID string
	Fence                       uint64
	LeaseUntil                  time.Time
	Job                         *review.JobClaim
}

type DispatchOutcome struct {
	Response         []byte
	ResponseReceived bool
	ResponseDigest   string
	ElapsedMS        int64
	// The current provider contract returns text, not trustworthy usage receipts.
	UsageKnown bool
}

type Option func(*Orchestrator)

func WithOperatorCeilings(limits *review.ResourceLimits) Option {
	return func(o *Orchestrator) {
		if limits != nil {
			copy := limits.Clone()
			o.operatorCeilings = &copy
		}
	}
}

func WithBudgetAuthority(authority BudgetAuthority) Option {
	return func(o *Orchestrator) { o.budgetAuthority = authority }
}

func (o *Orchestrator) OperatorCeilings() *review.ResourceLimits {
	if o == nil || o.operatorCeilings == nil {
		return nil
	}
	copy := o.operatorCeilings.Clone()
	return &copy
}

type budgetError struct{ err error }

func (e *budgetError) Error() string { return "orchestrator: budget: " + e.err.Error() }
func (e *budgetError) Unwrap() error { return e.err }

// IsBudgetError identifies terminal accounting failures across caller wrappers.
func IsBudgetError(err error) bool {
	var budget *budgetError
	return errors.As(err, &budget)
}
func fatalDispatch(err error) bool {
	return IsBudgetError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (o *Orchestrator) dispatch(ctx context.Context, rc *review.Context, spec ModelSpec, role ModelRole, step string, req LLMRequest, invoke func(context.Context) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	enforced := rc != nil && (rc.Source.Policy.Enforced || rc.Source.Policy.Mode == "enforce")
	if !enforced && o.operatorCeilings == nil {
		return invoke(ctx)
	}
	deny := func(message string) (string, error) { return "", &budgetError{errors.New(message)} }
	if o.operatorCeilings == nil || o.budgetAuthority == nil {
		return deny("durable reservation authority and operator ceilings are required")
	}
	if enforced && (rc.Source.Policy.Limits == nil || rc.Source.Policy.Digest == "" || !reflect.DeepEqual(rc.Source.Policy.OperatorCeilings, o.operatorCeilings)) {
		return deny("policy limits or current operator binding missing; readmission required")
	}
	limits := []review.ResourceLimits{o.operatorCeilings.Clone()}
	if enforced {
		limits = append(limits, rc.Source.Policy.Limits.Clone())
	}
	active := int64(1<<63 - 1)
	for _, limit := range limits {
		for _, scope := range []review.ResourceLimitScope{limit.Attempt, limit.Change, limit.Project} {
			if scope.ModelCalls <= 0 || scope.InputTokens <= 0 || scope.OutputTokens <= 0 || scope.ActiveMS <= 0 || scope.MoneyMicro < 0 {
				return deny("scope has no model capacity")
			}
			if req.MaxTokens <= 0 || int64(req.MaxTokens) > scope.OutputTokens {
				return deny("requested output exceeds scope ceiling")
			}
			if scope.ActiveMS < active {
				active = scope.ActiveMS
			}
		}
	}
	if active > int64((1<<63-1)/time.Millisecond) {
		return deny("active time bound overflows duration")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return deny(fmt.Sprintf("cannot bind request: %v", err))
	}
	digest := sha256.Sum256(payload)
	var actionBytes [16]byte
	if _, err := rand.Read(actionBytes[:]); err != nil {
		return deny("cannot create action identity")
	}
	requestDigest := "sha256:" + hex.EncodeToString(digest[:])
	intent := DispatchIntent{ActionID: hex.EncodeToString(actionBytes[:]), Provider: spec.Provider, Model: spec.Model, Role: string(role), Step: step, Payload: payload, RequestDigest: requestDigest, OperatorCeilings: o.operatorCeilings.Clone()}
	intent.Job = review.JobClaimFromContext(ctx)
	if rc != nil {
		intent.Attempt = rc.Source.Attempt
		intent.Execution = rc.Source.Execution
		if rc.Source.Attestation != nil {
			copy := *rc.Source.Attestation
			intent.Attestation = &copy
		}
		if enforced {
			copy := rc.Source.Policy.Limits.Clone()
			intent.PolicyLimits = &copy
		}
		intent.PolicyDigest = rc.Source.Policy.Digest
	}
	permit, err := o.budgetAuthority.ReserveAndBegin(ctx, intent)
	if err != nil {
		return "", &budgetError{fmt.Errorf("reservation denied: %w", err)}
	}
	if permit.ID == "" || permit.RequestDigest != requestDigest {
		return deny("invalid reservation receipt")
	}
	if permit.AlreadyStarted {
		return deny("action already started; reconcile without resending")
	}
	if permit.ActiveMS < 0 || permit.ActiveMS > active {
		return deny("invalid reserved active-time bound")
	}
	if permit.ActiveMS > 0 {
		active = permit.ActiveMS
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(active)*time.Millisecond)
	defer cancel()
	if !permit.NotAfter.IsZero() {
		deadlineCtx, stop := context.WithDeadline(callCtx, permit.NotAfter)
		defer stop()
		callCtx = deadlineCtx
	}
	start := time.Now()
	var result string
	if err = callCtx.Err(); err == nil {
		result, err = invoke(callCtx)
	}
	responseDigest := sha256.Sum256([]byte(result))
	outcome := DispatchOutcome{Response: []byte(result), ResponseReceived: err == nil, ResponseDigest: "sha256:" + hex.EncodeToString(responseDigest[:]), ElapsedMS: time.Since(start).Milliseconds()}
	recordCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if recordErr := o.budgetAuthority.RecordOutcome(recordCtx, permit, outcome); recordErr != nil {
		return "", &budgetError{fmt.Errorf("outcome not durably recorded: %w", recordErr)}
	}
	if callCtx.Err() != nil {
		return "", &budgetError{callCtx.Err()}
	}
	return result, err
}
