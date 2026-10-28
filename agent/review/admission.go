package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Admission is trusted application intake, not an HTTP or model-authored command.
type Admission struct {
	Request                   Request
	Change                    ChangeKey
	Attestation               SnapshotAttestation
	Policy                    PolicyProjection
	Execution                 ExecutionContext
	IntentKind                WorkIntentKind
	ExpectedGeneration        uint64
	DeliveryID, PayloadDigest string
}

func NewEphemeralReceipt(input Admission, now time.Time) (IntakeReceipt, error) {
	e := input.Execution
	if e.Mode != ExecutionEphemeral || e.Persistence != PersistenceJobLocal || e.PipelineID == "" || e.JobID == "" {
		return IntakeReceipt{}, fmt.Errorf("ephemeral intake requires explicit per-job execution identity")
	}
	if e.DeadlineAt.IsZero() || !now.Before(e.DeadlineAt) {
		return IntakeReceipt{}, fmt.Errorf("ephemeral execution deadline expired")
	}
	id, err := NewOpaqueID("attempt-")
	if err != nil {
		return IntakeReceipt{}, err
	}
	work, err := NewOpaqueID("intent-")
	if err != nil {
		return IntakeReceipt{}, err
	}
	a := AttemptIdentity{AttemptID: id, WorkIntentID: work, IntentKind: input.IntentKind, Change: input.Change, Snapshot: input.Attestation.Snapshot, PolicyDigest: input.Policy.Digest, Generation: 1, Version: 1, State: AttemptAccepted, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if _, err = NewVerifiedContext(input.Request, a, e, input.Attestation); err != nil {
		return IntakeReceipt{}, err
	}
	return IntakeReceipt{Attempt: a, Execution: e, Attestation: input.Attestation, Policy: Source{Policy: input.Policy}.Clone().Policy}, nil
}

type IntakeReceipt struct {
	Attempt     AttemptIdentity
	Execution   ExecutionContext
	Attestation SnapshotAttestation
	Policy      PolicyProjection
	Duplicate   bool
}

type AttemptIntake interface {
	ObserveGeneration(context.Context, ChangeKey) (uint64, error)
	Admit(context.Context, Admission) (IntakeReceipt, error)
}

func NewOpaqueID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

// ProviderHost comes from trusted endpoint configuration, never a report URL.
func ProviderHost(provider, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid trusted provider endpoint")
	}
	host := strings.ToLower(u.Host)
	if provider == "github" && host == "api.github.com" {
		host = "github.com"
	}
	return host, nil
}
