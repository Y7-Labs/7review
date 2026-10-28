package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
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
