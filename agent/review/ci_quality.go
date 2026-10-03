package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const MaxCIQualityArtifactBytes = 4 << 20

type CIQualityArtifact struct {
	SchemaVersion int               `json:"schema_version"`
	Kind          string            `json:"kind"`
	CheckID       string            `json:"check_id"`
	MethodID      string            `json:"method_id"`
	Scope         string            `json:"scope"`
	Status        string            `json:"status"`
	Summary       string            `json:"summary"`
	Signals       []CIQualitySignal `json:"signals"`
}

type CIQualitySignal struct {
	Key            string   `json:"key"`
	RuleID         string   `json:"rule_id"`
	Severity       Severity `json:"severity"`
	Strength       string   `json:"strength"`
	Message        string   `json:"message"`
	InChangedScope bool     `json:"in_changed_scope"`
}

type ImportedCIQualityEvidence struct {
	Kind       string
	Check      Check
	Signals    []CIQualitySignal
	Provenance CIArtifactProvenance
	verified   bool
}

func (e ImportedCIQualityEvidence) Validate() error {
	if !e.verified || !e.Provenance.ProviderVerified || !canonicalDigestPattern.MatchString(e.Provenance.ArtifactDigest) {
		return errors.New("CI quality evidence was not produced by verified import")
	}
	if err := e.Check.Validate(); err != nil {
		return err
	}
	for i, signal := range e.Signals {
		if err := validateCIQualitySignal(i, signal); err != nil {
			return err
		}
	}
	return nil
}

func ImportCIQualityEvidence(data []byte, provenance CIArtifactProvenance, snapshot SnapshotIdentity, execution ExecutionContext) (ImportedCIQualityEvidence, error) {
	if len(data) == 0 {
		return ImportedCIQualityEvidence{}, errors.New("CI quality artifact is empty")
	}
	if len(data) > MaxCIQualityArtifactBytes {
		return ImportedCIQualityEvidence{}, fmt.Errorf("CI quality artifact exceeds %d bytes", MaxCIQualityArtifactBytes)
	}
	if err := provenance.Validate(snapshot, execution); err != nil {
		return ImportedCIQualityEvidence{}, err
	}
	sum := sha256.Sum256(data)
	actualDigest := "sha256:" + hex.EncodeToString(sum[:])
	if provenance.ArtifactDigest != actualDigest {
		return ImportedCIQualityEvidence{}, errors.New("CI quality artifact digest does not match provenance")
	}

	var artifact CIQualityArtifact
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return ImportedCIQualityEvidence{}, fmt.Errorf("decode CI quality artifact: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ImportedCIQualityEvidence{}, err
	}
	if err := validateCIQualityArtifact(artifact); err != nil {
		return ImportedCIQualityEvidence{}, err
	}

	evidenceRef := fmt.Sprintf("ci:%s/%s:%s", provenance.PipelineID, provenance.JobID, provenance.ArtifactDigest)
	check := Check{
		ID: artifact.CheckID, MethodID: artifact.MethodID, Scope: artifact.Scope,
		ApplicabilityReason: "verified " + artifact.Kind + " artifact imported from CI",
		EvidenceRefs:        []string{evidenceRef}, StatusReason: strings.TrimSpace(artifact.Summary),
	}
	switch artifact.Status {
	case "passed":
		check.Status = CheckSatisfied
	case "failed":
		check.Status = CheckViolated
	case "error":
		check.Status = CheckUnknown
	case "skipped":
		check.Status = CheckNotApplicable
	}
	if err := check.Validate(); err != nil {
		return ImportedCIQualityEvidence{}, fmt.Errorf("CI quality check: %w", err)
	}
	return ImportedCIQualityEvidence{
		Kind: artifact.Kind, Check: check,
		Signals: append([]CIQualitySignal(nil), artifact.Signals...), Provenance: provenance, verified: true,
	}, nil
}

func validateCIQualityArtifact(artifact CIQualityArtifact) error {
	if artifact.SchemaVersion != 1 {
		return fmt.Errorf("unsupported CI quality artifact schema version %d", artifact.SchemaVersion)
	}
	switch artifact.Kind {
	case "test", "lint", "security", "coverage":
	default:
		return fmt.Errorf("unsupported CI quality artifact kind %q", artifact.Kind)
	}
	switch artifact.Status {
	case "passed", "failed":
	case "error", "skipped":
		if strings.TrimSpace(artifact.Summary) == "" {
			return errors.New("error or skipped CI quality artifact requires a summary")
		}
	default:
		return fmt.Errorf("unsupported CI quality artifact status %q", artifact.Status)
	}
	if strings.TrimSpace(artifact.CheckID) == "" || strings.TrimSpace(artifact.MethodID) == "" || strings.TrimSpace(artifact.Scope) == "" {
		return errors.New("CI quality artifact requires check, method and scope")
	}
	if len(artifact.Signals) > 4096 {
		return errors.New("CI quality artifact exceeds 4096 signals")
	}
	seen := make(map[string]bool, len(artifact.Signals))
	for i, signal := range artifact.Signals {
		if seen[signal.Key] {
			return fmt.Errorf("duplicate CI quality signal key %q", signal.Key)
		}
		seen[signal.Key] = true
		if err := validateCIQualitySignal(i, signal); err != nil {
			return err
		}
	}
	return nil
}

func validateCIQualitySignal(index int, signal CIQualitySignal) error {
	if strings.TrimSpace(signal.Key) == "" || strings.TrimSpace(signal.RuleID) == "" || strings.TrimSpace(signal.Message) == "" {
		return fmt.Errorf("CI quality signal %d requires key, rule and message", index)
	}
	switch signal.Severity {
	case SeverityInfo, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical:
	default:
		return fmt.Errorf("CI quality signal %d has unsupported severity %q", index, signal.Severity)
	}
	switch signal.Strength {
	case "confirmed", "human_check", "note":
	default:
		return fmt.Errorf("CI quality signal %d has unsupported strength %q", index, signal.Strength)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode CI quality artifact trailer: %w", err)
	}
	return errors.New("CI quality artifact contains multiple JSON values")
}
