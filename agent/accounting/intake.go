package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

var ErrStaleIntake = errors.New("canonical intake generation changed during acquisition")
var ErrIntakeConflict = errors.New("delivery identity conflicts with canonical payload")

func (a *Authority) trackingKey(change review.ChangeKey) string {
	raw, _ := json.Marshal(struct {
		Boundary, Project string
		Change            review.ChangeKey
	}{a.cfg.BoundaryID, a.cfg.ProjectID, change})
	return hash(raw)
}

func (a *Authority) ObserveGeneration(ctx context.Context, change review.ChangeKey) (uint64, error) {
	if err := change.Validate(); err != nil {
		return 0, err
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var generation int64
	err = tx.QueryRowContext(ctx, `SELECT generation FROM budget_heads WHERE id=$1`, a.trackingKey(change)).Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if generation < 0 {
		return 0, errors.New("invalid stored generation")
	}
	return uint64(generation), nil
}

func (a *Authority) Admit(ctx context.Context, input review.Admission) (review.IntakeReceipt, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return review.IntakeReceipt{}, err
	}
	defer tx.Rollback()
	receipt, err := a.admitTx(ctx, tx, input)
	if err != nil && !errors.Is(err, ErrIntakeConflict) {
		return review.IntakeReceipt{}, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return review.IntakeReceipt{}, commitErr
	}
	return receipt, err
}

func (a *Authority) admitTx(ctx context.Context, tx *sql.Tx, input review.Admission) (review.IntakeReceipt, error) {
	input.Execution.DeadlineAt = input.Execution.DeadlineAt.UTC()
	input.Attestation.VerifiedAt = input.Attestation.VerifiedAt.UTC()
	if input.Policy.Limits == nil || input.Policy.OperatorCeilings == nil || !input.Policy.Enforced || input.Policy.Mode != "enforce" {
		return review.IntakeReceipt{}, errors.New("canonical intake requires enforced bound policy")
	}
	if input.IntentKind != review.IntentAutomatic && input.IntentKind != review.IntentManual && input.IntentKind != review.IntentHistorical {
		return review.IntakeReceipt{}, errors.New("unsupported coordinated intake intent")
	}
	if input.ExpectedGeneration > math.MaxInt64 {
		return review.IntakeReceipt{}, errors.New("generation overflow")
	}
	if input.Execution.Mode == review.ExecutionEphemeral && (!validID(input.Execution.PipelineID) || !validID(input.Execution.JobID)) {
		return review.IntakeReceipt{}, errors.New("ephemeral intake requires pipeline and job identity")
	}
	id, err := review.NewOpaqueID("attempt-")
	if err != nil {
		return review.IntakeReceipt{}, err
	}
	workID, err := review.NewOpaqueID("intent-")
	if err != nil {
		return review.IntakeReceipt{}, err
	}
	if input.DeliveryID == "" {
		if input.Job != nil {
			input.DeliveryID = input.Job.RunID
		} else {
			input.DeliveryID, err = review.NewOpaqueID("manual-")
			if err != nil {
				return review.IntakeReceipt{}, err
			}
		}
	}
	if !validID(input.DeliveryID) {
		return review.IntakeReceipt{}, errors.New("invalid intake delivery identity")
	}
	payloadDigest := input.PayloadDigest
	if payloadDigest == "" {
		raw, err := json.Marshal(input.Request)
		if err != nil {
			return review.IntakeReceipt{}, err
		}
		payloadDigest = hash(raw)
	}
	if !canonicalDigest(payloadDigest) {
		return review.IntakeReceipt{}, errors.New("invalid intake payload digest")
	}
	fingerprintRaw, _ := json.Marshal(struct {
		Payload, Policy                   string
		Snapshot                          review.SnapshotIdentity
		Kind                              review.WorkIntentKind
		Mode                              review.ExecutionMode
		Producer, Pipeline, Job, Boundary string
	}{payloadDigest, input.Policy.Digest, input.Attestation.Snapshot, input.IntentKind, input.Execution.Mode, input.Execution.ProducerID, input.Execution.PipelineID, input.Execution.JobID, input.Execution.TrustedBoundaryID})
	fingerprint := hash(fingerprintRaw)
	keyRaw, _ := json.Marshal([]string{a.trackingKey(input.Change), string(input.Execution.Mode), input.Execution.ProducerID, input.DeliveryID})
	key := hash(keyRaw)
	if err = a.validateJobAdmission(ctx, tx, input); err != nil {
		return review.IntakeReceipt{}, err
	}
	tracking := input.Execution.Mode == review.ExecutionService && input.IntentKind != review.IntentHistorical
	var generation int64
	var predecessor sql.NullString
	trackID := a.trackingKey(input.Change)
	if tracking {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_heads(id,generation) VALUES($1,0) ON CONFLICT(id) DO NOTHING`, trackID); err != nil {
			return review.IntakeReceipt{}, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT generation,current_attempt_id FROM budget_heads WHERE id=$1 FOR UPDATE`, trackID).Scan(&generation, &predecessor); err != nil {
			return review.IntakeReceipt{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return review.IntakeReceipt{}, err
	}
	var priorFingerprint string
	var receiptJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT fingerprint,receipt FROM budget_intakes WHERE id=$1`, key).Scan(&priorFingerprint, &receiptJSON)
	if err == nil {
		if priorFingerprint != fingerprint {
			if _, err = tx.ExecContext(ctx, `INSERT INTO budget_intake_conflicts(id,intake_id,fingerprint) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, hash([]byte(key+fingerprint)), key, fingerprint); err != nil {
				return review.IntakeReceipt{}, err
			}
			return review.IntakeReceipt{}, ErrIntakeConflict
		}
		var receipt review.IntakeReceipt
		if err = json.Unmarshal(receiptJSON, &receipt); err != nil {
			return review.IntakeReceipt{}, err
		}
		receipt.Duplicate = true
		if err = linkJob(ctx, tx, input, receipt); err != nil {
			return review.IntakeReceipt{}, err
		}
		return receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return review.IntakeReceipt{}, err
	}
	if tracking && uint64(generation) != input.ExpectedGeneration {
		return review.IntakeReceipt{}, ErrStaleIntake
	}
	if generation == math.MaxInt64 {
		return review.IntakeReceipt{}, errors.New("generation exhausted")
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return review.IntakeReceipt{}, err
	}
	attempt := review.AttemptIdentity{AttemptID: id, WorkIntentID: workID, IntentKind: input.IntentKind, Change: input.Change, Snapshot: input.Attestation.Snapshot, PolicyDigest: input.Policy.Digest, Generation: uint64(generation) + 1, Version: 1, State: review.AttemptAccepted, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if tracking {
		attempt.PredecessorAttemptID = predecessor.String
	}
	if _, err = review.NewVerifiedContext(input.Request, attempt, input.Execution, input.Attestation); err != nil {
		return review.IntakeReceipt{}, err
	}
	i := orchestrator.DispatchIntent{Attempt: attempt, Execution: input.Execution, Attestation: &input.Attestation, PolicyDigest: input.Policy.Digest, PolicyLimits: input.Policy.Limits, OperatorCeilings: *input.Policy.OperatorCeilings}
	binding, err := a.binding(i)
	if err != nil {
		return review.IntakeReceipt{}, err
	}
	if !now.Before(input.Execution.DeadlineAt) {
		return review.IntakeReceipt{}, errors.New("intake execution deadline expired")
	}
	if tracking && input.IntentKind == review.IntentAutomatic && predecessor.Valid {
		var previousJSON []byte
		if err = tx.QueryRowContext(ctx, `SELECT receipt FROM budget_intakes WHERE attempt_id=$1 LIMIT 1`, predecessor.String).Scan(&previousJSON); err != nil {
			return review.IntakeReceipt{}, err
		}
		var previous review.IntakeReceipt
		if err = json.Unmarshal(previousJSON, &previous); err != nil {
			return review.IntakeReceipt{}, err
		}
		if previous.Attempt.Snapshot == attempt.Snapshot && previous.Policy.Digest == input.Policy.Digest && previous.Execution.ComparisonTree == input.Execution.ComparisonTree {
			if _, err = tx.ExecContext(ctx, `INSERT INTO budget_intakes(id,fingerprint,attempt_id,receipt) VALUES($1,$2,$3,$4)`, key, fingerprint, previous.Attempt.AttemptID, previousJSON); err != nil {
				return review.IntakeReceipt{}, err
			}
			previous.Duplicate = true
			if err = linkJob(ctx, tx, input, previous); err != nil {
				return review.IntakeReceipt{}, err
			}
			return previous, nil
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_attempts(id,binding_digest,binding,tracking_id) VALUES($1,$2,$3,$4)`, id, hash(binding), binding, nullableTracking(tracking, trackID)); err != nil {
		return review.IntakeReceipt{}, err
	}
	if tracking {
		if predecessor.Valid {
			if _, err = tx.ExecContext(ctx, `UPDATE budget_attempts SET active=false WHERE id=$1`, predecessor.String); err != nil {
				return review.IntakeReceipt{}, err
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE budget_heads SET generation=$2,current_attempt_id=$3 WHERE id=$1`, trackID, attempt.Generation, id); err != nil {
			return review.IntakeReceipt{}, err
		}
	}
	receipt := review.IntakeReceipt{Attempt: attempt, Execution: input.Execution, Attestation: input.Attestation, Policy: review.Source{Policy: input.Policy}.Clone().Policy}
	receiptJSON, err = json.Marshal(receipt)
	if err != nil {
		return review.IntakeReceipt{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_intakes(id,fingerprint,attempt_id,receipt) VALUES($1,$2,$3,$4)`, key, fingerprint, id, receiptJSON); err != nil {
		return review.IntakeReceipt{}, err
	}
	if err = linkJob(ctx, tx, input, receipt); err != nil {
		return review.IntakeReceipt{}, err
	}
	return receipt, nil
}

func nullableTracking(tracking bool, id string) any {
	if tracking {
		return id
	}
	return nil
}
