package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/tools"
)

type FrozenSnapshotReader interface {
	Snapshot(context.Context) (review.SnapshotIdentity, error)
	tools.RepositoryFileReader
}

// FrozenPolicyAdmission binds project policy to the immutable base revision
// while analysis runs against the separately attested head or merge tree.
type FrozenPolicyAdmission struct {
	Path                string
	Reader              FrozenSnapshotReader
	RuntimeCapabilities []string
	RuntimeCeilings     *policy.LimitsV2
	Now                 func() time.Time
}

func (a FrozenPolicyAdmission) Evaluate(ctx context.Context, req review.Request, scm *review.SCMContext) (TrustedPolicyResult, error) {
	if a.Reader == nil {
		return TrustedPolicyResult{}, fmt.Errorf("frozen policy: repository reader is not configured")
	}
	now := time.Now().UTC()
	if a.Now != nil {
		now = a.Now().UTC()
	}
	snapshot, err := a.Reader.Snapshot(ctx)
	if err != nil {
		return TrustedPolicyResult{}, fmt.Errorf("frozen policy: snapshot: %w", err)
	}
	if scm == nil || scm.DiffRefs.BaseSHA != snapshot.BaseRevision || scm.DiffRefs.HeadSHA != snapshot.HeadRevision {
		return TrustedPolicyResult{}, fmt.Errorf("frozen policy: SCM context does not match the immutable snapshot")
	}
	attestation := review.SnapshotAttestation{
		Snapshot: snapshot, ProducerID: req.Provider + ":frozen-git",
		ComparisonTree: snapshot.AnalysisRevision(), VerifiedAt: now, Verified: true,
	}
	if err := attestation.ValidateSnapshot(); err != nil {
		return TrustedPolicyResult{}, fmt.Errorf("frozen policy: attestation: %w", err)
	}
	data, err := a.Reader.ReadRepositoryFile(ctx, snapshot.RepositoryID, snapshot.BaseRevision, a.Path)
	if err != nil {
		return TrustedPolicyResult{}, fmt.Errorf("frozen policy: read base policy: %w", err)
	}
	config, err := policy.Decode(a.Path, data)
	if err != nil {
		return TrustedPolicyResult{}, err
	}
	sum := sha256.Sum256(data)
	source := policy.Source{
		RepositoryID: snapshot.RepositoryID, Revision: snapshot.BaseRevision,
		Digest: "sha256:" + hex.EncodeToString(sum[:]), Trust: policy.TrustBaseSnapshot,
	}
	changedPaths := make([]string, 0, len(scm.Files))
	for _, file := range scm.Files {
		changedPaths = append(changedPaths, firstNonEmpty(file.NewPath, file.OldPath))
	}
	effective, err := policy.CompileBound(config, source, attestation, policy.CompileContext{
		ProjectID: snapshot.RepositoryID, ChangedPaths: changedPaths, Branch: req.TargetBranch,
		Author: req.Author, Labels: append([]string(nil), req.Labels...),
		RuntimeAllowed: append([]string(nil), a.RuntimeCapabilities...), RuntimeCeilings: a.RuntimeCeilings,
		EvaluationTime: now,
	})
	if err != nil {
		return TrustedPolicyResult{}, err
	}
	return TrustedPolicyResult{
		Mode: "enforce", Attestation: attestation, Source: source, Effective: &effective, Enforced: true,
		Trigger: policy.TriggerDecision{Accepted: true, Reasons: []string{"ephemeral CI review was explicitly requested"}},
	}, nil
}
