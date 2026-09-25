package policy

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

type TrustKind string

const (
	TrustBaseSnapshot            TrustKind = "base_snapshot"
	TrustOperatorApprovedInitial TrustKind = "operator_approved_initial"
)

type Source struct {
	RepositoryID string
	Revision     string
	Digest       string
	Trust        TrustKind
	ApprovedBy   string
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func CompileBound(config ReviewConfigV2, source Source, attestation review.SnapshotAttestation, ctx CompileContext) (EffectivePolicy, error) {
	if err := validateSource(source, attestation); err != nil {
		return EffectivePolicy{}, err
	}
	effective, err := Compile(config, ctx)
	if err != nil {
		return EffectivePolicy{}, err
	}
	effective.Explanation = append([]ExplanationEntry{{
		Field: "policy_source", Value: source.Digest, Source: string(source.Trust),
		Reason: "configuration bound to an immutable trusted source", Precedence: 1<<31 - 1,
	}}, effective.Explanation...)
	return effective, nil
}

func validateSource(source Source, attestation review.SnapshotAttestation) error {
	if !attestation.Verified {
		return fmt.Errorf("policy: snapshot attestation is not verified")
	}
	if source.RepositoryID == "" || source.RepositoryID != attestation.Snapshot.RepositoryID {
		return fmt.Errorf("policy: source repository does not match attested snapshot")
	}
	if !digestPattern.MatchString(source.Digest) {
		return fmt.Errorf("policy: source digest must be canonical sha256")
	}
	switch source.Trust {
	case TrustBaseSnapshot:
		if strings.TrimSpace(attestation.Snapshot.BaseRevision) == "" || source.Revision != attestation.Snapshot.BaseRevision {
			return fmt.Errorf("policy: trusted base policy revision does not match attested base")
		}
	case TrustOperatorApprovedInitial:
		if attestation.Snapshot.LocalSnapshotDigest == "" || attestation.Snapshot.BaseRevision != "" || strings.TrimSpace(source.ApprovedBy) == "" {
			return fmt.Errorf("policy: initial local policy requires a base-less local snapshot and accountable approval")
		}
	default:
		return fmt.Errorf("policy: unsupported trust kind %q", source.Trust)
	}
	return nil
}
