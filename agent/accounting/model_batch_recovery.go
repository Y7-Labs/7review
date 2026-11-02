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

// The job lock serializes cursor moves with claim/Begin. Concurrent outcomes
// only move sending to recorded or uncertain; neither is reclaimed here.
func (a *Authority) reclaimBatchCursorTx(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) (*review.JobClaim, bool, error) {
	var token string
	var grant sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT i.claim_token,j.recovery_action_id FROM budget_jobs j JOIN budget_inbox i USING(run_id) WHERE i.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND i.state='running' AND i.deadline>clock_timestamp() AND j.attempt_id=$4 AND j.dispatch_allowed AND j.phase IN ('admitted','effect_started') FOR UPDATE OF i,j`, i.Job.RunID, a.cfg.BoundaryID, a.cfg.ProjectID, i.Attempt.AttemptID).Scan(&token, &grant)
	if err != nil {
		return nil, true, err
	}
	seal, err := a.readModelBatchSealTx(ctx, tx, i.Job.RunID)
	if err != nil {
		return nil, true, err
	}
	if seal == nil {
		return nil, false, nil
	}
	members := map[string]bool{}
	for _, slot := range seal.Slots {
		members[slot.ActionID] = true
	}
	if !members[i.ActionID] {
		return nil, false, nil
	}
	if grant.Valid && !members[grant.String] {
		return nil, true, errors.New("batch cursor has advanced beyond initial slots")
	}
	original, _, err := a.storedAction(ctx, tx, seal.Slots[0].ActionID)
	if err != nil {
		return nil, true, err
	}
	origin := original.Job.Token
	if (grant.Valid && token == origin) || (!grant.Valid && token != origin) {
		return nil, true, errors.New("batch recovery epoch has no valid cursor binding")
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM budget_actions WHERE attempt_id=$1`, seal.AttemptID).Scan(&count); err != nil {
		return nil, true, err
	}
	if count != len(seal.Slots) {
		return nil, true, errors.New("unsealed sibling effects block batch recovery")
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, true, err
	}
	firstPrepared := ""
	size := 0
	for _, slot := range seal.Slots {
		var state string
		var outcome []byte
		var fence int64
		var sent sql.NullInt64
		var worker, epoch sql.NullString
		var until sql.NullTime
		var incident bool
		if err = tx.QueryRowContext(ctx, `SELECT a.state,a.outcome,a.integrity_incident,l.fence,l.sending_fence,l.worker_id,l.lease_until,l.job_token FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.id=$1 AND a.fenced`, slot.ActionID).Scan(&state, &outcome, &incident, &fence, &sent, &worker, &until, &epoch); err != nil {
			return nil, true, err
		}
		if incident {
			return nil, true, errors.New("batch integrity incident blocks recovery")
		}
		size += len(outcome)
		if size > 64<<20 {
			return nil, true, errors.New("batch recovery evidence exceeds bound")
		}
		switch state {
		case "prepared":
			if sent.Valid || outcome != nil {
				return nil, true, errors.New("prepared batch slot has external progress")
			}
			if worker.Valid {
				if fence <= 0 || !until.Valid || now.Before(until.Time) || !epoch.Valid || (epoch.String != origin && epoch.String != token) {
					return nil, true, errors.New("batch still has active or invalid prepared ownership")
				}
			} else if fence != 0 || until.Valid || epoch.Valid {
				return nil, true, errors.New("batch lease provenance is inconsistent")
			}
			if firstPrepared == "" {
				firstPrepared = slot.ActionID
			}
		case "result_recorded":
			if fence <= 0 || !worker.Valid || !until.Valid || !sent.Valid || sent.Int64 != fence || !epoch.Valid || (epoch.String != origin && epoch.String != token) {
				return nil, true, errors.New("batch result has unproven sending epoch")
			}
			var out orchestrator.DispatchOutcome
			if err = json.Unmarshal(outcome, &out); err != nil {
				return nil, true, err
			}
			if !out.ResponseReceived || hash(out.Response) != out.ResponseDigest {
				return nil, true, errors.New("batch result is not a known intact response")
			}
		default:
			return nil, true, errors.New("sending or uncertain batch effect requires reconciliation")
		}
	}
	if firstPrepared != i.ActionID {
		return nil, true, errors.New("recovery must claim the first unfinished sealed slot")
	}
	if !grant.Valid {
		token, err = review.NewOpaqueID("recovered-job-")
		if err != nil {
			return nil, true, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET claim_token=$2,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID, token); err != nil {
			return nil, true, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET recovery_action_id=$2,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID, i.ActionID); err != nil {
		return nil, true, err
	}
	return &review.JobClaim{RunID: i.Job.RunID, Token: token}, true, nil
}

func (a *Authority) batchOriginEpochTx(ctx context.Context, tx *sql.Tx, artifact review.ModelReduction) (sql.NullString, error) {
	seal, err := a.readModelBatchSealTx(ctx, tx, artifact.Source.Run.ID)
	if err != nil {
		return sql.NullString{}, err
	}
	if seal == nil || seal.GroupDigest != artifact.GroupDigest {
		return sql.NullString{}, nil
	}
	if len(seal.Slots) != len(artifact.Inputs) {
		return sql.NullString{}, errors.New("terminal batch omitted sealed inputs")
	}
	for index, slot := range seal.Slots {
		if slot.ActionID != artifact.Inputs[index].ActionID {
			return sql.NullString{}, errors.New("terminal batch reordered sealed inputs")
		}
	}
	i, _, err := a.storedAction(ctx, tx, seal.Slots[0].ActionID)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: i.Job.Token, Valid: true}, nil
}
