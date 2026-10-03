package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

func ciGateFixture(t *testing.T) (policy.EffectivePolicy, review.AssessmentProjection, time.Time) {
	t.Helper()
	effective := policy.EffectivePolicy{
		SchemaVersion: 2, Digest: "sha256:" + strings.Repeat("f", 64), GateMode: "blocking",
		QualityGate: policy.QualityGateV2{
			RequiredCoverage: []string{"tests"}, MinSeverity: "high", MinStrength: "confirmed",
			BaselineMode: "all", ContextName: "7review/quality",
		},
	}
	now := time.Now().UTC()
	assessment := review.AssessmentProjection{
		ID: "assessment-1", Version: 1, Purpose: "assessment", SnapshotDigest: "sha256:snapshot",
		PolicyDigest: effective.Digest, Completeness: review.AssessmentComplete, Risk: "high", CreatedAt: now,
	}
	return effective, assessment, now
}

func importedCheck(t *testing.T, id string, status review.CheckStatus, signals ...review.CIQualitySignal) review.ImportedCIQualityEvidence {
	t.Helper()
	artifactStatus := map[review.CheckStatus]string{
		review.CheckSatisfied: "passed", review.CheckViolated: "failed",
		review.CheckUnknown: "error", review.CheckNotApplicable: "skipped",
	}[status]
	artifact := review.CIQualityArtifact{
		SchemaVersion: 1, Kind: "test", CheckID: id, MethodID: "ci/" + id,
		Scope: "project", Status: artifactStatus, Summary: "verified CI result", Signals: signals,
	}
	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	now := time.Now().UTC()
	snapshot := review.SnapshotIdentity{RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)}
	execution := review.ExecutionContext{Mode: review.ExecutionEphemeral, ProducerID: "ci", PipelineID: "run", JobID: id, ComparisonTree: "tree", Persistence: review.PersistenceJobLocal, TrustedBoundaryID: "ci:org/repo", DeadlineAt: now.Add(time.Hour)}
	provenance := review.CIArtifactProvenance{PipelineID: "run", JobID: id, SourceRevision: "head", ComparisonTree: "tree", ArtifactDigest: "sha256:" + hex.EncodeToString(sum[:]), ProducerID: "ci", VerifiedAt: now, ProviderVerified: true}
	imported, err := review.ImportCIQualityEvidence(data, provenance, snapshot, execution)
	if err != nil {
		t.Fatal(err)
	}
	return imported
}

func TestEvaluateImportedCIGatePassesVerifiedRequiredCoverage(t *testing.T) {
	effective, assessment, now := ciGateFixture(t)
	coverage, gate, err := EvaluateImportedCIGate(effective, assessment, "sha256:"+strings.Repeat("a", 64), []review.ImportedCIQualityEvidence{
		importedCheck(t, "tests", review.CheckSatisfied),
	}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if !coverage.Complete || gate.Outcome != review.GatePass || policy.GateExitCode(gate) != 0 {
		t.Fatalf("verified required evidence should pass: coverage=%#v gate=%#v", coverage, gate)
	}
}

func TestEvaluateImportedCIGateFailsClosedForMissingOrSkippedEvidence(t *testing.T) {
	effective, assessment, now := ciGateFixture(t)
	for name, evidence := range map[string][]review.ImportedCIQualityEvidence{
		"missing": nil,
		"skipped": {importedCheck(t, "tests", review.CheckNotApplicable)},
	} {
		t.Run(name, func(t *testing.T) {
			coverage, gate, err := EvaluateImportedCIGate(effective, assessment, "sha256:"+strings.Repeat("a", 64), evidence, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			if coverage.Complete || gate.Outcome != review.GateIncomplete || policy.GateExitCode(gate) != 2 {
				t.Fatalf("missing required evidence must be incomplete: coverage=%#v gate=%#v", coverage, gate)
			}
		})
	}
}

func TestEvaluateImportedCIGateReportsValidatedViolation(t *testing.T) {
	effective, assessment, now := ciGateFixture(t)
	signal := review.CIQualitySignal{Key: "sast:1", RuleID: "unsafe-input", Severity: review.SeverityCritical, Strength: "confirmed", Message: "unsafe command input", InChangedScope: true}
	_, gate, err := EvaluateImportedCIGate(effective, assessment, "sha256:"+strings.Repeat("a", 64), []review.ImportedCIQualityEvidence{
		importedCheck(t, "tests", review.CheckSatisfied), importedCheck(t, "security", review.CheckViolated, signal),
	}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if gate.Outcome != review.GateViolations || !slices.Contains(gate.ViolatedRuleIDs, "unsafe-input") || policy.GateExitCode(gate) != 1 {
		t.Fatalf("validated CI signal must violate a blocking gate: %#v", gate)
	}
}

func TestEvaluateImportedCIGateRejectsDuplicateChecks(t *testing.T) {
	effective, assessment, now := ciGateFixture(t)
	_, _, err := EvaluateImportedCIGate(effective, assessment, "sha256:"+strings.Repeat("a", 64), []review.ImportedCIQualityEvidence{
		importedCheck(t, "tests", review.CheckSatisfied), importedCheck(t, "tests", review.CheckViolated),
	}, nil, now)
	if err == nil || !strings.Contains(err.Error(), "duplicate CI check") {
		t.Fatalf("duplicate check identities must fail: %v", err)
	}
}
