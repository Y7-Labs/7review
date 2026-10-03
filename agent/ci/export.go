package ci

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

const artifactSchemaVersion = 1

type artifactDescriptor struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Digest    string `json:"digest"`
	Bytes     int    `json:"bytes"`
}

type runManifest struct {
	SchemaVersion     int                     `json:"schema_version"`
	Kind              string                  `json:"kind"`
	Snapshot          review.SnapshotIdentity `json:"snapshot"`
	Execution         review.ExecutionContext `json:"execution"`
	PolicyDigest      string                  `json:"policy_digest"`
	GateMode          review.GateMode         `json:"gate_mode"`
	GateOutcome       review.GateOutcome      `json:"gate_outcome"`
	ExitCode          int                     `json:"exit_code"`
	Artifacts         []artifactDescriptor    `json:"artifacts"`
	IncompleteReasons []string                `json:"incomplete_reasons,omitempty"`
	Exclusions        []string                `json:"exclusions"`
	StartedAt         time.Time               `json:"started_at"`
	CompletedAt       time.Time               `json:"completed_at"`
}

type assessmentArtifact struct {
	SchemaVersion int                         `json:"schema_version"`
	Kind          string                      `json:"kind"`
	Snapshot      review.SnapshotIdentity     `json:"snapshot"`
	Attestation   review.SnapshotAttestation  `json:"attestation"`
	Execution     review.ExecutionContext     `json:"execution"`
	Policy        exportedEffectivePolicy     `json:"policy"`
	Assessment    review.AssessmentProjection `json:"assessment"`
	Findings      []exportedFinding           `json:"findings"`
	CIEvidence    []exportedCIQualityEvidence `json:"ci_evidence,omitempty"`
}

type exportedEffectivePolicy struct {
	SchemaVersion     int                       `json:"schema_version"`
	Digest            string                    `json:"digest"`
	Methods           []string                  `json:"methods"`
	RequiredChecks    []string                  `json:"required_checks"`
	RiskFloor         string                    `json:"risk_floor"`
	IndependentReview bool                      `json:"independent_review"`
	GateMode          string                    `json:"gate_mode"`
	QualityGate       policy.QualityGateV2      `json:"quality_gate"`
	MatchedPacks      []string                  `json:"matched_packs,omitempty"`
	Explanation       []policy.ExplanationEntry `json:"explanation"`
}

type exportedFinding struct {
	ID                string                    `json:"id"`
	Severity          review.Severity           `json:"severity"`
	Title             string                    `json:"title"`
	Description       string                    `json:"description"`
	Suggestion        string                    `json:"suggestion"`
	Location          exportedLocation          `json:"location"`
	Confidence        float64                   `json:"confidence"`
	FindingType       string                    `json:"finding_type,omitempty"`
	Strength          string                    `json:"strength,omitempty"`
	EvidenceAuthority string                    `json:"evidence_authority,omitempty"`
	Citations         []review.EvidenceCitation `json:"citations,omitempty"`
	ValidationStatus  string                    `json:"validation_status,omitempty"`
	ValidationReason  string                    `json:"validation_reason,omitempty"`
}

type exportedLocation struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

type exportedCIQualityEvidence struct {
	Kind       string                      `json:"kind"`
	Check      review.Check                `json:"check"`
	Signals    []review.CIQualitySignal    `json:"signals,omitempty"`
	Provenance review.CIArtifactProvenance `json:"provenance"`
}

type coverageArtifact struct {
	SchemaVersion int                       `json:"schema_version"`
	Kind          string                    `json:"kind"`
	PolicyDigest  string                    `json:"policy_digest"`
	Coverage      review.CoverageProjection `json:"coverage"`
}

type gateArtifact struct {
	SchemaVersion int               `json:"schema_version"`
	Kind          string            `json:"kind"`
	Gate          review.GateResult `json:"gate"`
	ExitCode      int               `json:"exit_code"`
}

func Export(result Result, outputDir string) error {
	if outputDir == "" {
		return errors.New("CI artifact output directory is required")
	}
	if result.Gate.Outcome == "" || result.Assessment.ID == "" {
		return errors.New("CI result is not complete enough to export")
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return fmt.Errorf("create CI artifact directory: %w", err)
	}
	ciEvidence := make([]exportedCIQualityEvidence, 0, len(result.ImportedEvidence))
	for _, evidence := range result.ImportedEvidence {
		ciEvidence = append(ciEvidence, exportedCIQualityEvidence{Kind: evidence.Kind, Check: evidence.Check, Signals: evidence.Signals, Provenance: evidence.Provenance})
	}
	findings := make([]exportedFinding, 0, len(result.Source.Findings))
	for _, finding := range result.Source.Findings {
		findings = append(findings, exportedFinding{
			ID: finding.ID, Severity: finding.Severity, Title: finding.Title, Description: finding.Description,
			Suggestion: finding.Suggestion, Location: exportedLocation{Path: finding.Location.Path, Line: finding.Location.Line}, Confidence: finding.Confidence,
			FindingType: finding.FindingType, Strength: finding.Strength, EvidenceAuthority: finding.EvidenceAuthority,
			Citations: finding.Citations, ValidationStatus: finding.ValidationStatus, ValidationReason: finding.ValidationReason,
		})
	}
	effective := result.EffectivePolicy
	exportedPolicy := exportedEffectivePolicy{
		SchemaVersion: effective.SchemaVersion, Digest: effective.Digest, Methods: effective.Methods,
		RequiredChecks: effective.RequiredChecks, RiskFloor: effective.RiskFloor,
		IndependentReview: effective.IndependentReview, GateMode: effective.GateMode,
		QualityGate: effective.QualityGate, MatchedPacks: effective.MatchedPacks, Explanation: effective.Explanation,
	}
	files := []struct {
		name      string
		mediaType string
		value     any
		raw       []byte
	}{
		{name: "assessment.json", mediaType: "application/vnd.7review.assessment+json", value: assessmentArtifact{SchemaVersion: artifactSchemaVersion, Kind: "assessment", Snapshot: result.Snapshot, Attestation: result.Attestation, Execution: result.Execution, Policy: exportedPolicy, Assessment: result.Assessment, Findings: findings, CIEvidence: ciEvidence}},
		{name: "coverage.json", mediaType: "application/vnd.7review.coverage+json", value: coverageArtifact{SchemaVersion: artifactSchemaVersion, Kind: "coverage", PolicyDigest: result.EffectivePolicy.Digest, Coverage: result.Coverage}},
		{name: "gate.json", mediaType: "application/vnd.7review.gate+json", value: gateArtifact{SchemaVersion: artifactSchemaVersion, Kind: "gate", Gate: result.Gate, ExitCode: ExitCode(result)}},
		{name: "report.md", mediaType: "text/markdown; charset=utf-8", raw: []byte(result.Report)},
	}
	descriptors := make([]artifactDescriptor, 0, len(files))
	for i := range files {
		data := files[i].raw
		if data == nil {
			var err error
			data, err = marshalCanonical(files[i].value)
			if err != nil {
				return fmt.Errorf("encode %s: %w", files[i].name, err)
			}
		}
		if err := writeAtomicFile(outputDir, files[i].name, data); err != nil {
			return err
		}
		descriptors = append(descriptors, artifactDescriptor{Name: files[i].name, MediaType: files[i].mediaType, Digest: artifactDigest(data), Bytes: len(data)})
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Name < descriptors[j].Name })
	manifest := runManifest{
		SchemaVersion: artifactSchemaVersion, Kind: "run_manifest", Snapshot: result.Snapshot, Execution: result.Execution,
		PolicyDigest: result.EffectivePolicy.Digest, GateMode: result.Gate.Mode, GateOutcome: result.Gate.Outcome,
		ExitCode: ExitCode(result), Artifacts: descriptors, IncompleteReasons: uniqueStrings(result.IncompleteReasons),
		Exclusions: []string{"no SCM check, comment, status, or annotation was published", "no sidecar, channel, or long-running server was started", "merge authority remains with a human"},
		StartedAt:  result.StartedAt, CompletedAt: result.CompletedAt,
	}
	data, err := marshalCanonical(manifest)
	if err != nil {
		return err
	}
	return writeAtomicFile(outputDir, "run-manifest.json", data)
}

func marshalCanonical(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeAtomicFile(directory, name string, data []byte) error {
	temporary, err := os.CreateTemp(directory, "."+name+"-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", name, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(directory, name)); err != nil {
		return fmt.Errorf("publish %s: %w", name, err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr := directoryHandle.Sync()
	closeErr := directoryHandle.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func artifactDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
