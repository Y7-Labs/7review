package review

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SnapshotAttestation struct {
	Snapshot       SnapshotIdentity `json:"snapshot"`
	ProducerID     string           `json:"producer_id"`
	ComparisonTree string           `json:"comparison_tree"`
	VerifiedAt     time.Time        `json:"verified_at"`
	Verified       bool             `json:"verified"`
}

func (a SnapshotAttestation) Validate(change ChangeKey) error {
	if !a.Verified {
		return errors.New("snapshot attestation is not verified")
	}
	if err := a.Snapshot.Validate(); err != nil {
		return err
	}
	if err := change.Validate(); err != nil {
		return err
	}
	if change.RepositoryID != a.Snapshot.RepositoryID {
		return errors.New("snapshot attestation repository does not match change")
	}
	if strings.TrimSpace(a.ProducerID) == "" || strings.TrimSpace(a.ComparisonTree) == "" || a.VerifiedAt.IsZero() {
		return errors.New("verified snapshot requires producer, comparison tree and verification time")
	}
	return nil
}

func (a SnapshotAttestation) Digest() (string, error) {
	if err := a.Snapshot.Validate(); err != nil {
		return "", err
	}
	canonical := strings.Join([]string{
		a.Snapshot.RepositoryID,
		a.Snapshot.BaseRevision,
		a.Snapshot.HeadRevision,
		a.Snapshot.LocalSnapshotDigest,
		a.Snapshot.FileManifestDigest,
		a.ProducerID,
		a.ComparisonTree,
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type ReadinessStatus string

const (
	ReadinessPresent       ReadinessStatus = "present"
	ReadinessMissing       ReadinessStatus = "missing"
	ReadinessNotApplicable ReadinessStatus = "not_applicable"
	ReadinessContradictory ReadinessStatus = "contradictory"
)

type ReadinessField struct {
	Status     ReadinessStatus `json:"status"`
	Provenance []string        `json:"provenance,omitempty"`
	Reason     string          `json:"reason"`
}

func (f ReadinessField) Validate() error {
	switch f.Status {
	case ReadinessPresent, ReadinessMissing, ReadinessNotApplicable, ReadinessContradictory:
	default:
		return fmt.Errorf("unsupported readiness status %q", f.Status)
	}
	if strings.TrimSpace(f.Reason) == "" {
		return errors.New("readiness reason is required")
	}
	if (f.Status == ReadinessPresent || f.Status == ReadinessContradictory) && len(f.Provenance) == 0 {
		return errors.New("present or contradictory readiness requires provenance")
	}
	return nil
}

type ReadinessProjection struct {
	IntentSummary      ReadinessField `json:"intent_summary"`
	AcceptanceCriteria ReadinessField `json:"acceptance_criteria"`
	TestEvidence       ReadinessField `json:"test_evidence"`
}

type EvidenceVerification string

const (
	EvidenceVerified   EvidenceVerification = "verified"
	EvidenceUnverified EvidenceVerification = "unverified"
	EvidenceUnknown    EvidenceVerification = "unknown"
)

type TestExecutionEvidence struct {
	Revision     string               `json:"revision"`
	CheckName    string               `json:"check_name"`
	Result       string               `json:"result"`
	SourceRef    string               `json:"source_ref"`
	ExecutedAt   time.Time            `json:"executed_at"`
	ProducerID   string               `json:"producer_id"`
	Verification EvidenceVerification `json:"verification"`
}

func (e TestExecutionEvidence) Validate() error {
	if strings.TrimSpace(e.Revision) == "" || strings.TrimSpace(e.CheckName) == "" || strings.TrimSpace(e.Result) == "" || strings.TrimSpace(e.SourceRef) == "" || strings.TrimSpace(e.ProducerID) == "" || e.ExecutedAt.IsZero() {
		return errors.New("test evidence requires revision, check, result, source, execution time and producer")
	}
	switch e.Verification {
	case EvidenceVerified, EvidenceUnverified, EvidenceUnknown:
		return nil
	default:
		return fmt.Errorf("unsupported evidence verification %q", e.Verification)
	}
}

func TestEvidenceReadiness(snapshot SnapshotIdentity, evidence []TestExecutionEvidence) ReadinessField {
	if len(evidence) == 0 {
		return ReadinessField{Status: ReadinessMissing, Reason: "no test execution evidence supplied"}
	}
	for _, item := range evidence {
		if item.Validate() == nil && item.Verification == EvidenceVerified && item.Revision == snapshot.HeadRevision {
			return ReadinessField{Status: ReadinessPresent, Provenance: []string{item.SourceRef}, Reason: "verified test execution is bound to the current revision"}
		}
	}
	return ReadinessField{Status: ReadinessMissing, Reason: "test evidence is unverified, invalid, or bound to another revision"}
}

func (r ReadinessProjection) Validate() error {
	fields := []struct {
		name  string
		value ReadinessField
	}{{"intent_summary", r.IntentSummary}, {"acceptance_criteria", r.AcceptanceCriteria}, {"test_evidence", r.TestEvidence}}
	for _, field := range fields {
		if err := field.value.Validate(); err != nil {
			return fmt.Errorf("%s: %w", field.name, err)
		}
	}
	return nil
}
