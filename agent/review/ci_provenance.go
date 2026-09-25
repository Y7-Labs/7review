package review

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type CIArtifactProvenance struct {
	PipelineID       string    `json:"pipeline_id"`
	JobID            string    `json:"job_id"`
	SourceRevision   string    `json:"source_revision"`
	ComparisonTree   string    `json:"comparison_tree"`
	ArtifactDigest   string    `json:"artifact_digest"`
	ProducerID       string    `json:"producer_id"`
	VerifiedAt       time.Time `json:"verified_at"`
	ProviderVerified bool      `json:"provider_verified"`
}

func (p CIArtifactProvenance) Validate(snapshot SnapshotIdentity, execution ExecutionContext) error {
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("CI artifact snapshot: %w", err)
	}
	if err := execution.Validate(); err != nil {
		return fmt.Errorf("CI artifact execution: %w", err)
	}
	if !canonicalDigestPattern.MatchString(snapshot.FileManifestDigest) {
		return errors.New("CI artifact snapshot requires a canonical manifest digest")
	}
	if !p.ProviderVerified {
		return errors.New("CI artifact provenance is not provider verified")
	}
	if strings.TrimSpace(p.PipelineID) == "" || strings.TrimSpace(p.JobID) == "" || strings.TrimSpace(p.ProducerID) == "" || p.VerifiedAt.IsZero() {
		return errors.New("CI artifact provenance requires pipeline, job, producer and verification time")
	}
	if !canonicalDigestPattern.MatchString(p.ArtifactDigest) {
		return errors.New("CI artifact requires a canonical digest")
	}
	if p.SourceRevision != snapshot.HeadRevision {
		return fmt.Errorf("CI artifact revision does not match snapshot head")
	}
	if p.ComparisonTree != execution.ComparisonTree {
		return fmt.Errorf("CI artifact comparison tree does not match execution")
	}
	if execution.PipelineID != "" && p.PipelineID != execution.PipelineID {
		return fmt.Errorf("CI artifact pipeline does not match execution")
	}
	if execution.JobID != "" && p.JobID != execution.JobID {
		return fmt.Errorf("CI artifact job does not match execution")
	}
	return nil
}
