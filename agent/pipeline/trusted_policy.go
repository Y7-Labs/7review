package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/tools"
)

type RepositoryPolicyReader interface {
	ReadRepositoryFile(context.Context, string, string, string, string) ([]byte, error)
}

type TrustedPolicyAdmission interface {
	Evaluate(context.Context, review.Request, *review.SCMContext) (TrustedPolicyResult, error)
}

type TrustedPolicyResult struct {
	Mode        string
	Attestation review.SnapshotAttestation
	Source      policy.Source
	Effective   *policy.EffectivePolicy
	Trigger     policy.TriggerDecision
	Warning     string
	Enforced    bool
}

type SCMPolicyAdmission struct {
	Mode                string
	Path                string
	Reader              RepositoryPolicyReader
	RuntimeCapabilities []string
	RuntimeCeilings     *policy.LimitsV2
	Now                 func() time.Time
}

func (a SCMPolicyAdmission) Evaluate(ctx context.Context, req review.Request, scm *review.SCMContext) (TrustedPolicyResult, error) {
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	if mode == "" || mode == "legacy" {
		return TrustedPolicyResult{Mode: "legacy", Trigger: policy.TriggerDecision{Accepted: true, Reasons: []string{"legacy admission policy"}}}, nil
	}
	if mode != "preview" && mode != "enforce" {
		return TrustedPolicyResult{}, fmt.Errorf("trusted policy: unsupported mode %q", a.Mode)
	}
	if a.Reader == nil {
		return TrustedPolicyResult{}, fmt.Errorf("trusted policy: repository reader is not configured")
	}
	now := time.Now().UTC()
	if a.Now != nil {
		now = a.Now().UTC()
	}
	_, attestation, err := review.AttestSCMRequest(req, scm, now)
	if err != nil {
		if mode == "preview" {
			return TrustedPolicyResult{
				Mode: mode, Warning: "trusted policy attestation failed: " + err.Error(),
				Trigger: policy.TriggerDecision{Accepted: true, Reasons: []string{"preview attestation failure did not alter admission"}},
			}, nil
		}
		return TrustedPolicyResult{}, fmt.Errorf("trusted policy: attest source: %w", err)
	}
	result := TrustedPolicyResult{Mode: mode, Attestation: attestation, Enforced: mode == "enforce"}
	data, err := a.Reader.ReadRepositoryFile(ctx, req.Provider, attestation.Snapshot.RepositoryID, attestation.Snapshot.BaseRevision, a.Path)
	if err != nil {
		if mode == "preview" {
			if errors.Is(err, tools.ErrRepositoryFileNotFound) {
				result.Warning = "trusted policy file is absent at the base revision"
			} else {
				result.Warning = "trusted policy could not be read in preview mode: " + err.Error()
			}
			result.Trigger = policy.TriggerDecision{Accepted: true, Reasons: []string{"preview read failure did not alter admission"}}
			return result, nil
		}
		return TrustedPolicyResult{}, fmt.Errorf("trusted policy: read base policy: %w", err)
	}
	config, err := policy.Decode(a.Path, data)
	if err != nil {
		if mode == "preview" {
			result.Warning = err.Error()
			result.Trigger = policy.TriggerDecision{Accepted: true, Reasons: []string{"invalid preview policy did not alter admission"}}
			return result, nil
		}
		return TrustedPolicyResult{}, fmt.Errorf("trusted policy: %w", err)
	}
	sum := sha256.Sum256(data)
	result.Source = policy.Source{
		RepositoryID: attestation.Snapshot.RepositoryID,
		Revision:     attestation.Snapshot.BaseRevision,
		Digest:       "sha256:" + hex.EncodeToString(sum[:]),
		Trust:        policy.TrustBaseSnapshot,
	}
	if req.EventAction == "manual" || req.DeliveryID == "" {
		result.Trigger = policy.TriggerDecision{Accepted: true, Reasons: []string{"manual review bypasses automatic trigger selection"}}
	} else {
		result.Trigger = policy.EvaluateTrigger(config.Triggers, policy.TriggerInput{
			IsUpdate: isUpdateAction(req.EventAction), IsDraft: req.Draft,
			Branch: req.TargetBranch, Author: req.Author, Labels: req.Labels,
		})
	}
	changedPaths := make([]string, 0, len(scm.Files))
	for _, file := range scm.Files {
		changedPaths = append(changedPaths, firstNonEmpty(file.NewPath, file.OldPath))
	}
	effective, err := policy.CompileBound(config, result.Source, attestation, policy.CompileContext{
		ProjectID:       attestation.Snapshot.RepositoryID,
		ChangedPaths:    changedPaths,
		Branch:          req.TargetBranch,
		Author:          req.Author,
		Labels:          req.Labels,
		RuntimeAllowed:  append([]string(nil), a.RuntimeCapabilities...),
		RuntimeCeilings: a.RuntimeCeilings,
		EvaluationTime:  now,
	})
	if err != nil {
		if mode == "preview" {
			result.Warning = err.Error()
			result.Trigger.Accepted = true
			result.Trigger.Reasons = append(result.Trigger.Reasons, "preview compilation failure did not alter admission")
			return result, nil
		}
		return TrustedPolicyResult{}, fmt.Errorf("trusted policy: compile: %w", err)
	}
	result.Effective = &effective
	return result, nil
}

func isUpdateAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "synchronize", "update", "updated", "reopen", "reopened":
		return true
	default:
		return false
	}
}

func projectTrustedPolicy(result TrustedPolicyResult) review.PolicyProjection {
	projection := review.PolicyProjection{
		Mode: result.Mode, SourceRevision: result.Source.Revision, SourceDigest: result.Source.Digest,
		TriggerAccepted: result.Trigger.Accepted, TriggerReasons: append([]string(nil), result.Trigger.Reasons...),
	}
	if result.Warning != "" {
		projection.Warnings = []string{result.Warning}
	}
	if result.Effective == nil {
		return projection
	}
	projection.Digest = result.Effective.Digest
	projection.Methods = append([]string(nil), result.Effective.Methods...)
	projection.RequiredChecks = append([]string(nil), result.Effective.RequiredChecks...)
	projection.MatchedPacks = append([]string(nil), result.Effective.MatchedPacks...)
	projection.RiskFloor = result.Effective.RiskFloor
	projection.IndependentReview = result.Effective.IndependentReview
	for _, decision := range result.Effective.Explanation {
		projection.Decisions = append(projection.Decisions, review.PolicyDecision{
			Field: decision.Field, Value: decision.Value, Source: decision.Source,
			Reason: decision.Reason, Precedence: decision.Precedence,
		})
	}
	return projection
}
