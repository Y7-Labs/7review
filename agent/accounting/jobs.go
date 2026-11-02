package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

// Lock ordering for linked execution is inbox/job -> head -> attempt -> scopes.
func (a *Authority) lockJob(ctx context.Context, tx *sql.Tx, claim *review.JobClaim) (review.Request, time.Time, error) {
	var req review.Request
	if claim == nil || !validID(claim.RunID) || !validID(claim.Token) {
		return req, time.Time{}, errors.New("durable job claim required")
	}
	var raw []byte
	var deadline time.Time
	err := tx.QueryRowContext(ctx, `SELECT i.request,i.deadline FROM budget_inbox i JOIN budget_jobs j ON j.run_id=i.run_id WHERE i.boundary_id=$1 AND i.project_id=$2 AND i.run_id=$3 AND i.claim_token=$4 AND i.state='running' AND i.deadline>clock_timestamp() AND j.phase NOT IN ('finished','legacy_unknown') FOR UPDATE OF i,j`, a.cfg.BoundaryID, a.cfg.ProjectID, claim.RunID, claim.Token).Scan(&raw, &deadline)
	if err != nil {
		return req, deadline, errors.New("durable job ownership unavailable or expired")
	}
	err = json.Unmarshal(raw, &req)
	return req, deadline, err
}

func (a *Authority) guardJobDispatch(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) error {
	var linked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_jobs WHERE attempt_id=$1)`, i.Attempt.AttemptID).Scan(&linked); err != nil {
		return err
	}
	if !linked {
		if a.cfg.RequireServiceJobs && i.Execution.Mode == review.ExecutionService {
			return errors.New("service dispatch requires a linked durable job")
		}
		if i.Job != nil {
			return errors.New("dispatch job is not linked to attempt")
		}
		return nil
	}
	if _, _, err := a.lockJob(ctx, tx, i.Job); err != nil {
		return err
	}
	var allowed bool
	var recovery sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT dispatch_allowed AND attempt_id=$2,recovery_action_id FROM budget_jobs WHERE run_id=$1`, i.Job.RunID, i.Attempt.AttemptID).Scan(&allowed, &recovery); err != nil {
		return err
	}
	if !allowed {
		return errors.New("job cannot dispatch this attempt")
	}
	if recovery.Valid && recovery.String != i.ActionID {
		return errors.New("recovered job is confined to its prepared action; validated continuation required")
	}
	var child bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_model_continuations WHERE child_action_id=$1)`, i.ActionID).Scan(&child); err != nil {
		return err
	}
	if child {
		allowed, err := a.confinedContinuationTx(ctx, tx, i)
		if err != nil {
			return err
		}
		if !allowed {
			return errors.New("prepared child lacks current parent authority")
		}
	}
	return nil
}

func (a *Authority) validateJobAdmission(ctx context.Context, tx *sql.Tx, input review.Admission) error {
	if input.Job == nil {
		if a.cfg.RequireServiceJobs && input.Execution.Mode == review.ExecutionService {
			return errors.New("service admission requires a durable job claim")
		}
		return nil
	}
	req, deadline, err := a.lockJob(ctx, tx, input.Job)
	if err != nil {
		return err
	}
	if input.Execution.Mode != review.ExecutionService || input.Execution.Persistence != review.PersistenceDurableService || !input.Execution.DeadlineAt.Equal(deadline) {
		return errors.New("job execution authority/deadline mismatch")
	}
	// SCM enrichment may fill content fields, but cannot rebind transport identity.
	r := input.Request
	if req.Provider != r.Provider || req.ProjectID != r.ProjectID || req.Repository != r.Repository || req.ChangeID != r.ChangeID || req.MRIID != r.MRIID || req.DeliveryID != r.DeliveryID || req.PayloadDigest != r.PayloadDigest || req.EventAction != r.EventAction || req.SourceSHA != r.SourceSHA || req.TargetSHA != r.TargetSHA {
		return errors.New("job admission does not match accepted request")
	}
	return nil
}

func linkJob(ctx context.Context, tx *sql.Tx, input review.Admission, receipt review.IntakeReceipt) error {
	if input.Job == nil {
		return nil
	}
	var prior sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id FROM budget_jobs WHERE run_id=$1`, input.Job.RunID).Scan(&prior); err != nil {
		return err
	}
	if prior.Valid && prior.String != receipt.Attempt.AttemptID {
		return errors.New("job already linked to another attempt")
	}
	if prior.Valid {
		return nil
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET attempt_id=$2,receipt=$3,dispatch_allowed=$4,phase='admitted',updated_at=clock_timestamp() WHERE run_id=$1`, input.Job.RunID, receipt.Attempt.AttemptID, raw, !receipt.Duplicate)
	return err
}
