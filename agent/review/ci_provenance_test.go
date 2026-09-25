package review

import (
	"strings"
	"testing"
	"time"
)

func TestCIArtifactProvenanceBindsArtifactToExecution(t *testing.T) {
	now := time.Now().UTC()
	snapshot := SnapshotIdentity{
		RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head",
		FileManifestDigest: "sha256:" + strings.Repeat("a", 64),
	}
	execution := ExecutionContext{
		Mode: ExecutionEphemeral, ProducerID: "github-actions", PipelineID: "run-10", JobID: "review",
		ComparisonTree: "merge-tree:10", Persistence: PersistenceJobLocal,
		TrustedBoundaryID: "github:org/repo", DeadlineAt: now.Add(time.Hour),
	}
	provenance := CIArtifactProvenance{
		PipelineID: "run-10", JobID: "review", SourceRevision: "head", ComparisonTree: "merge-tree:10",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64), ProducerID: "github-actions",
		VerifiedAt: now, ProviderVerified: true,
	}
	if err := provenance.Validate(snapshot, execution); err != nil {
		t.Fatal(err)
	}

	stale := provenance
	stale.SourceRevision = "old-head"
	if err := stale.Validate(snapshot, execution); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("stale artifact must fail: %v", err)
	}
	unverified := provenance
	unverified.ProviderVerified = false
	if err := unverified.Validate(snapshot, execution); err == nil || !strings.Contains(err.Error(), "not provider verified") {
		t.Fatalf("unverified artifact must fail: %v", err)
	}
	wrongJob := provenance
	wrongJob.JobID = "other"
	if err := wrongJob.Validate(snapshot, execution); err == nil || !strings.Contains(err.Error(), "job") {
		t.Fatalf("artifact from another job must fail: %v", err)
	}
}
