package review

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func TestImportCIQualityEvidenceRequiresBoundProvenance(t *testing.T) {
	now := time.Now().UTC()
	data := []byte(`{"schema_version":1,"kind":"test","check_id":"correctness","method_id":"ci/tests","scope":"project","status":"passed","summary":"tests passed","signals":[]}`)
	sum := sha256.Sum256(data)
	snapshot := SnapshotIdentity{RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)}
	execution := ExecutionContext{
		Mode: ExecutionEphemeral, ProducerID: "github-actions", PipelineID: "run-10", JobID: "tests",
		ComparisonTree: "merge-tree:10", Persistence: PersistenceJobLocal,
		TrustedBoundaryID: "github:org/repo", DeadlineAt: now.Add(time.Hour),
	}
	provenance := CIArtifactProvenance{
		PipelineID: "run-10", JobID: "tests", SourceRevision: "head", ComparisonTree: "merge-tree:10",
		ArtifactDigest: "sha256:" + hex.EncodeToString(sum[:]), ProducerID: "github-actions", VerifiedAt: now, ProviderVerified: true,
	}
	imported, err := ImportCIQualityEvidence(data, provenance, snapshot, execution)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Check.Status != CheckSatisfied || len(imported.Check.EvidenceRefs) != 1 {
		t.Fatalf("verified artifact was not normalized: %#v", imported)
	}

	tampered := append([]byte(nil), data...)
	tampered[len(tampered)-2] = ' '
	if _, err := ImportCIQualityEvidence(tampered, provenance, snapshot, execution); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tampered artifact must fail provenance: %v", err)
	}
	provenance.ProviderVerified = false
	if _, err := ImportCIQualityEvidence(data, provenance, snapshot, execution); err == nil || !strings.Contains(err.Error(), "not provider verified") {
		t.Fatalf("unverified artifact must fail: %v", err)
	}
}

func TestImportCIQualityEvidenceRejectsUnknownShapeAndNormalizesFailure(t *testing.T) {
	now := time.Now().UTC()
	data := []byte(`{"schema_version":1,"kind":"security","check_id":"security","method_id":"ci/sast","scope":"project","status":"failed","summary":"one issue","signals":[{"key":"sast:1","rule_id":"unsafe-input","severity":"high","strength":"confirmed","message":"input reaches command","in_changed_scope":true}]}`)
	sum := sha256.Sum256(data)
	snapshot := SnapshotIdentity{RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)}
	execution := ExecutionContext{Mode: ExecutionEphemeral, ProducerID: "gitlab-ci", PipelineID: "20", JobID: "sast", ComparisonTree: "tree", Persistence: PersistenceJobLocal, TrustedBoundaryID: "gitlab:org/repo", DeadlineAt: now.Add(time.Hour)}
	provenance := CIArtifactProvenance{PipelineID: "20", JobID: "sast", SourceRevision: "head", ComparisonTree: "tree", ArtifactDigest: "sha256:" + hex.EncodeToString(sum[:]), ProducerID: "gitlab-ci", VerifiedAt: now, ProviderVerified: true}
	imported, err := ImportCIQualityEvidence(data, provenance, snapshot, execution)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Check.Status != CheckViolated || len(imported.Signals) != 1 || imported.Signals[0].RuleID != "unsafe-input" {
		t.Fatalf("failed artifact was not normalized: %#v", imported)
	}

	unknown := []byte(`{"schema_version":1,"kind":"test","check_id":"tests","method_id":"ci/tests","scope":"project","status":"passed","summary":"ok","signals":[],"command":"curl secret"}`)
	unknownSum := sha256.Sum256(unknown)
	provenance.ArtifactDigest = "sha256:" + hex.EncodeToString(unknownSum[:])
	if _, err := ImportCIQualityEvidence(unknown, provenance, snapshot, execution); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown artifact fields must fail closed: %v", err)
	}
}
