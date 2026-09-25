package review

import (
	"strings"
	"testing"
	"time"
)

func TestSnapshotAttestationBindsRepositoryAndImmutableInput(t *testing.T) {
	change := ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "org/repo", ChangeNumber: "42"}
	attestation := SnapshotAttestation{
		Snapshot:   SnapshotIdentity{RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)},
		ProducerID: "github-app:7review", ComparisonTree: "merge-tree:42", VerifiedAt: time.Now().UTC(), Verified: true,
	}
	if err := attestation.Validate(change); err != nil {
		t.Fatal(err)
	}
	first, err := attestation.Digest()
	if err != nil {
		t.Fatal(err)
	}
	attestation.Snapshot.HeadRevision = "other"
	second, err := attestation.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("snapshot digest must change with the bound revision")
	}
	attestation.Verified = false
	if err := attestation.Validate(change); err == nil {
		t.Fatal("unverified snapshot must be rejected")
	}
}

func TestReadinessRequiresReasonAndProvenance(t *testing.T) {
	projection := ReadinessProjection{
		IntentSummary:      ReadinessField{Status: ReadinessPresent, Provenance: []string{"request:description"}, Reason: "author supplied intent"},
		AcceptanceCriteria: ReadinessField{Status: ReadinessMissing, Reason: "no criteria supplied"},
		TestEvidence:       ReadinessField{Status: ReadinessNotApplicable, Reason: "documentation-only change"},
	}
	if err := projection.Validate(); err != nil {
		t.Fatal(err)
	}
	projection.IntentSummary.Provenance = nil
	if err := projection.Validate(); err == nil {
		t.Fatal("present readiness without provenance must fail")
	}
}

func TestScenario_S06_StaleTestProof(t *testing.T) {
	snapshot := SnapshotIdentity{RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "h2", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)}
	evidence := []TestExecutionEvidence{{Revision: "h1", CheckName: "test", Result: "passed", SourceRef: "ci:1", ExecutedAt: time.Now().UTC(), ProducerID: "github-actions", Verification: EvidenceVerified}}
	got := TestEvidenceReadiness(snapshot, evidence)
	if got.Status != ReadinessMissing || !strings.Contains(got.Reason, "another revision") {
		t.Fatalf("stale proof must remain missing: %#v", got)
	}
	evidence[0].Revision = "h2"
	got = TestEvidenceReadiness(snapshot, evidence)
	if got.Status != ReadinessPresent || len(got.Provenance) != 1 {
		t.Fatalf("current verified proof should be present: %#v", got)
	}
}

func TestScenario_S26_RepositoryIsolationAtVerifiedIntake(t *testing.T) {
	change := ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "org/a", ChangeNumber: "7"}
	attestation := SnapshotAttestation{
		Snapshot:   SnapshotIdentity{RepositoryID: "org/b", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)},
		ProducerID: "github", ComparisonTree: "merge-tree", VerifiedAt: time.Now().UTC(), Verified: true,
	}
	if err := attestation.Validate(change); err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("cross-repository attestation must be rejected: %v", err)
	}
}

func TestAttestSCMRequestIsOrderIndependentAndRejectsStaleWebhook(t *testing.T) {
	req := Request{Provider: "github", ProjectID: "org/repo", Repository: "org/repo", ChangeID: "7", SourceSHA: "head", TargetSHA: "base", WebURL: "https://github.com/org/repo/pull/7"}
	scm := &SCMContext{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DiffRefs: DiffRefs{BaseSHA: "base", HeadSHA: "head"}, Files: []ChangedFile{
		{NewPath: "b.go", Patch: "+b", Status: "modified"},
		{NewPath: "a.go", Patch: "+a", Status: "modified"},
	}}
	_, first, err := AttestSCMRequest(req, scm, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	scm.Files[0], scm.Files[1] = scm.Files[1], scm.Files[0]
	_, second, err := AttestSCMRequest(req, scm, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot.FileManifestDigest != second.Snapshot.FileManifestDigest {
		t.Fatal("manifest digest must not depend on provider file ordering")
	}
	req.SourceSHA = "stale-head"
	if _, _, err := AttestSCMRequest(req, scm, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "head revision") {
		t.Fatalf("stale webhook head must be rejected: %v", err)
	}
}
