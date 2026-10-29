package pipeline

import (
	"context"
	"fmt"
	"github.com/Y4NN777/7review/agent/review"
	"strconv"
	"time"
)

type CanonicalIntake struct {
	Authority     review.AttemptIntake
	ProviderHosts map[string]string
	Execution     review.ExecutionContext
	Now           func() time.Time
}

func (c *CanonicalIntake) change(req review.Request) (review.ChangeKey, error) {
	key := review.ChangeKey{Provider: req.Provider, Host: c.ProviderHosts[req.Provider], RepositoryID: req.ProjectID, ChangeNumber: req.ChangeID}
	host, err := review.ProviderHost(req.Provider, "https://"+key.Host)
	if err != nil || key.Host == "" {
		return review.ChangeKey{}, fmt.Errorf("canonical trusted provider host is required")
	}
	key.Host = host
	if key.ChangeNumber == "" && req.MRIID != 0 {
		key.ChangeNumber = strconv.Itoa(req.MRIID)
	}
	if err := key.Validate(); err != nil {
		return review.ChangeKey{}, fmt.Errorf("canonical intake: %w", err)
	}
	return key, nil
}

func (c *CanonicalIntake) admit(ctx context.Context, rc *review.Context, generation uint64) (bool, error) {
	change, err := c.change(rc.Request)
	if err != nil {
		return false, err
	}
	if rc.Source.Attestation == nil || !rc.Source.Policy.Enforced {
		return false, fmt.Errorf("canonical intake requires verified enforced policy")
	}
	execution := c.Execution
	execution.ComparisonTree = rc.Source.Attestation.ComparisonTree
	if deadline, ok := ctx.Deadline(); ok && (execution.DeadlineAt.IsZero() || deadline.Before(execution.DeadlineAt)) {
		execution.DeadlineAt = deadline.UTC()
	}
	kind := review.IntentAutomatic
	if rc.Request.EventAction == "manual" || rc.Request.DeliveryID == "" {
		kind = review.IntentManual
	}
	input := review.Admission{Request: rc.Request, Change: change, Attestation: *rc.Source.Attestation, Policy: rc.Source.Policy, Execution: execution, IntentKind: kind, ExpectedGeneration: generation, DeliveryID: rc.Request.DeliveryID, PayloadDigest: rc.Request.PayloadDigest}
	input.Job = review.JobClaimFromContext(ctx)
	var receipt review.IntakeReceipt
	if c.Authority != nil {
		receipt, err = c.Authority.Admit(ctx, input)
	} else {
		now := time.Now().UTC()
		if c.Now != nil {
			now = c.Now().UTC()
		}
		receipt, err = review.NewEphemeralReceipt(input, now)
	}
	if err != nil {
		return false, err
	}
	if receipt.Attempt.Change != change || receipt.Execution.Mode != execution.Mode || receipt.Execution.Persistence != execution.Persistence || receipt.Execution.ProducerID != execution.ProducerID || receipt.Execution.TrustedBoundaryID != execution.TrustedBoundaryID || receipt.Execution.PipelineID != execution.PipelineID || receipt.Execution.JobID != execution.JobID || receipt.Execution.DeadlineAt.After(execution.DeadlineAt) {
		return false, fmt.Errorf("canonical receipt changed trusted execution authority")
	}
	verified, err := review.NewVerifiedContext(rc.Request, receipt.Attempt, receipt.Execution, receipt.Attestation)
	if err != nil {
		return false, err
	}
	if receipt.Policy.Digest != rc.Source.Policy.Digest || receipt.Attempt.PolicyDigest != rc.Source.Policy.Digest || receipt.Attestation.Snapshot != rc.Source.Attestation.Snapshot {
		return false, fmt.Errorf("canonical receipt does not match admitted policy/snapshot")
	}
	rc.Source.Attempt = verified.Source.Attempt
	rc.Source.Execution = verified.Source.Execution
	rc.Source.Attestation = verified.Source.Attestation
	rc.Source.Investigation = verified.Source.Investigation
	return receipt.Duplicate, nil
}
