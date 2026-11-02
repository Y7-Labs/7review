package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Y4NN777/7review/agent/review"
)

var ErrModelJobNotFound = errors.New("no durable terminal model job")

func (a *Authority) CompleteModelJob(ctx context.Context, actionID string) (*review.ModelJobProjection, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	artifact, err := a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return nil, err
	}
	if artifact.State != review.ModelReductionValidated && artifact.State != review.ModelReductionBlocked {
		return nil, errors.New("model result still requires continuation")
	}
	id := reductionKey(artifact.Source.Run.ID, artifact.GroupDigest)
	var state, phase, token string
	var allowed, inTime bool
	var grant, terminal sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.state,j.phase,i.claim_token,j.dispatch_allowed,j.recovery_action_id,j.terminal_reduction_id,i.deadline>clock_timestamp() FROM budget_inbox i JOIN budget_jobs j USING(run_id) WHERE i.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND j.attempt_id=$4 FOR UPDATE OF i,j`, artifact.Source.Run.ID, a.cfg.BoundaryID, a.cfg.ProjectID, artifact.Source.Attempt.AttemptID).Scan(&state, &phase, &token, &allowed, &grant, &terminal, &inTime)
	if err != nil {
		return nil, err
	}
	if terminal.Valid {
		if terminal.String != id {
			return nil, errors.New("job has a different terminal result")
		}
		projection, err := a.lookupModelJobTx(ctx, tx, artifact.Source.Run.ID)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return projection, nil
	}
	wantState := "failed"
	if artifact.State == review.ModelReductionValidated {
		wantState = "completed"
	}
	backfill := phase == "finished" && state == wantState
	if !backfill && (state != "running" || phase != "effect_started" || !allowed || !inTime) {
		return nil, errors.New("terminal model job is unowned, expired or not active")
	}
	if !backfill {
		var tracking sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT tracking_id FROM budget_attempts WHERE id=$1`, artifact.Source.Attempt.AttemptID).Scan(&tracking); err != nil {
			return nil, err
		}
		if tracking.Valid {
			var current sql.NullString
			if err = tx.QueryRowContext(ctx, `SELECT current_attempt_id FROM budget_heads WHERE id=$1 FOR UPDATE`, tracking.String).Scan(&current); err != nil {
				return nil, err
			}
			if current.String != artifact.Source.Attempt.AttemptID {
				return nil, errors.New("terminal attempt is no longer current")
			}
		}
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT active FROM budget_attempts WHERE id=$1 FOR UPDATE`, artifact.Source.Attempt.AttemptID).Scan(&active); err != nil {
			return nil, err
		}
		if !active {
			return nil, errors.New("terminal attempt is closed")
		}
	}
	artifact, err = a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return nil, err
	}
	if err = a.checkTerminalLineageTx(ctx, tx, id, artifact); err != nil {
		return nil, err
	}
	var changed bool
	origin, err := a.batchOriginEpochTx(ctx, tx, artifact)
	if err != nil {
		return nil, err
	}
	if origin.Valid {
		if !grant.Valid && token != origin.String {
			return nil, errors.New("unconfined terminal batch changed original epoch")
		}
		if grant.Valid {
			var bound bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x JOIN budget_action_leases l ON l.action_id=x.action_id WHERE x.reduction_id=$1 AND x.action_id=$2 AND l.job_token=$3)`, id, grant.String, token).Scan(&bound); err != nil {
				return nil, err
			}
			if !bound {
				return nil, errors.New("terminal batch cursor has no current sending epoch")
			}
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x LEFT JOIN budget_action_leases l ON l.action_id=x.action_id WHERE x.reduction_id=$1 AND (l.action_id IS NULL OR l.job_token IS NULL OR (l.job_token IS DISTINCT FROM $2 AND l.job_token IS DISTINCT FROM $3) OR l.sending_fence IS NULL OR l.sending_fence<>l.fence))`, id, token, origin).Scan(&changed)
	if err != nil {
		return nil, err
	}
	if changed {
		return nil, errors.New("terminal model result belongs to a stale job epoch")
	}
	owned := !grant.Valid
	for _, input := range artifact.Inputs {
		if grant.String == input.ActionID {
			owned = true
		}
	}
	if !owned {
		return nil, errors.New("terminal result is not the current job cursor")
	}
	query := `SELECT EXISTS(SELECT 1 FROM budget_actions a WHERE a.attempt_id=$1 AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=$2 AND (a.state<>'result_recorded' OR NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x WHERE x.action_id=a.id)))`
	args := []any{artifact.Source.Attempt.AttemptID, artifact.Source.Run.ID}
	if !grant.Valid {
		query = `SELECT EXISTS(SELECT 1 FROM budget_actions a WHERE a.attempt_id=$1 AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=$2 AND NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x WHERE x.action_id=a.id AND x.reduction_id=$3))`
		args = append(args, id)
	}
	if err = tx.QueryRowContext(ctx, query, args...).Scan(&changed); err != nil {
		return nil, err
	}
	if changed {
		return nil, errors.New("unresolved sibling or unproven ancestry blocks terminal completion")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET terminal_reduction_id=$2,phase='finished',dispatch_allowed=false,updated_at=clock_timestamp() WHERE run_id=$1`, artifact.Source.Run.ID, id); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET state=$2,updated_at=clock_timestamp() WHERE run_id=$1`, artifact.Source.Run.ID, wantState); err != nil {
		return nil, err
	}
	projection, err := a.lookupModelJobTx(ctx, tx, artifact.Source.Run.ID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return projection, nil
}

func (a *Authority) LookupModelJob(ctx context.Context, runID string) (*review.ModelJobProjection, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := a.lookupModelJobTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

func (a *Authority) lookupModelJobTx(ctx context.Context, tx *sql.Tx, runID string) (*review.ModelJobProjection, error) {
	var id, actionID, state, phase string
	var allowed bool
	p := &review.ModelJobProjection{RunID: runID}
	err := tx.QueryRowContext(ctx, `SELECT j.terminal_reduction_id,(SELECT min(x.action_id) FROM budget_model_reduction_inputs x WHERE x.reduction_id=j.terminal_reduction_id),i.state,j.phase,j.dispatch_allowed,j.updated_at FROM budget_jobs j JOIN budget_inbox i USING(run_id) WHERE i.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND j.terminal_reduction_id IS NOT NULL`, runID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&id, &actionID, &state, &phase, &allowed, &p.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrModelJobNotFound
	}
	if err != nil {
		return nil, err
	}
	artifact, err := a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return nil, err
	}
	if reductionKey(runID, artifact.GroupDigest) != id || phase != "finished" || allowed || (artifact.State != review.ModelReductionValidated && artifact.State != review.ModelReductionBlocked) || (artifact.State == review.ModelReductionValidated && state != "completed") || (artifact.State == review.ModelReductionBlocked && state != "failed") {
		return nil, errors.New("terminal projection attribution or authority mismatch")
	}
	if err = a.checkTerminalLineageTx(ctx, tx, id, artifact); err != nil {
		return nil, err
	}
	p.Source = artifact.Source.Clone()
	p.Succeeded = artifact.State == review.ModelReductionValidated
	p.Reason = artifact.Reason
	return p, nil
}

// Historical projection verifies evidence, not current dispatch authority. Each
// typed edge must still bind the exact parent proof and frozen child plan.
func (a *Authority) checkTerminalLineageTx(ctx context.Context, tx *sql.Tx, id string, artifact review.ModelReduction) error {
	return a.checkModelLineageTx(ctx, tx, id, artifact, true)
}

func (a *Authority) checkModelLineageTx(ctx context.Context, tx *sql.Tx, id string, artifact review.ModelReduction, requireAllActions bool, preparedChild ...string) error {
	if _, err := a.readModelBatchSealTx(ctx, tx, artifact.Source.Run.ID); err != nil {
		return err
	}
	type group struct {
		id       string
		artifact review.ModelReduction
	}
	pending := []group{{id, artifact}}
	seen := map[string]bool{}
	var actions []string
	inputs := 0
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if seen[current.id] {
			return errors.New("cyclic or duplicated terminal ancestry")
		}
		seen[current.id] = true
		inputs += len(current.artifact.Inputs)
		if inputs > 128 {
			return errors.New("terminal ancestry exceeds proof bound")
		}
		if err := a.checkReductionProofTx(ctx, tx, current.id, current.artifact); err != nil {
			return err
		}
		for _, input := range current.artifact.Inputs {
			var sent bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_action_leases l JOIN budget_actions a ON a.id=l.action_id WHERE l.action_id=$1 AND a.fenced AND a.state='result_recorded' AND l.fence>0 AND l.sending_fence=l.fence AND l.worker_id IS NOT NULL AND l.lease_until IS NOT NULL AND l.job_token IS NOT NULL)`, input.ActionID).Scan(&sent); err != nil {
				return err
			}
			if !sent {
				return errors.New("terminal ancestry has no proven sending fence")
			}
			actions = append(actions, input.ActionID)
			i, _, err := a.storedAction(ctx, tx, input.ActionID)
			if err != nil {
				return err
			}
			cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
			if err != nil {
				return err
			}
			parentID, parent, found, err := a.readContinuationParentTx(ctx, tx, i)
			if err != nil {
				return err
			}
			if !found && cp.Cursor.Phase == review.ModelReviewBatches {
				continue
			}
			if !found {
				return errors.New("model ancestor has no continuation link")
			}
			pending = append(pending, group{parentID, parent})
		}
	}
	if !requireAllActions {
		return nil
	}
	if len(preparedChild) > 1 {
		return errors.New("ambiguous prepared child lineage")
	}
	if len(preparedChild) == 1 {
		i, _, err := a.storedAction(ctx, tx, preparedChild[0])
		if err != nil {
			return err
		}
		parent, _, found, err := a.readContinuationParentTx(ctx, tx, i)
		if err != nil {
			return err
		}
		if !found || parent != id {
			return errors.New("prepared exclusion is not an exact linked child")
		}
		actions = append(actions, preparedChild[0])
	}
	var unlinked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_actions a WHERE a.attempt_id=$1 AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=$2 AND NOT (a.id=ANY($3::text[])))`, artifact.Source.Attempt.AttemptID, artifact.Source.Run.ID, actions).Scan(&unlinked); err != nil {
		return err
	}
	if unlinked {
		return errors.New("terminal proof does not cover every job action")
	}
	return nil
}

func (a *Authority) CompletePendingModelJobs(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 128 {
		return 0, errors.New("invalid terminal projection batch bound")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT min(x.action_id)
FROM budget_model_reductions m JOIN budget_model_reduction_inputs x ON x.reduction_id=m.id
JOIN budget_jobs j ON j.run_id=m.run_id JOIN budget_inbox i ON i.run_id=j.run_id
JOIN budget_attempts t ON t.id=j.attempt_id
WHERE i.boundary_id=$1 AND i.project_id=$2 AND j.terminal_reduction_id IS NULL
AND m.state IN ('validated','blocked')
AND ((i.state='running' AND i.deadline>clock_timestamp() AND j.phase='effect_started' AND j.dispatch_allowed AND t.active
AND (t.tracking_id IS NULL OR EXISTS(SELECT 1 FROM budget_heads h WHERE h.id=t.tracking_id AND h.current_attempt_id=t.id)))
OR (j.phase='finished' AND ((i.state='completed' AND m.state='validated') OR (i.state='failed' AND m.state='blocked'))))
AND (j.recovery_action_id IS NULL OR EXISTS(SELECT 1 FROM budget_model_reduction_inputs own WHERE own.reduction_id=m.id AND own.action_id=j.recovery_action_id))
AND NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs e LEFT JOIN budget_action_leases l ON l.action_id=e.action_id
WHERE e.reduction_id=m.id AND (l.action_id IS NULL OR l.job_token IS NULL OR (l.job_token IS DISTINCT FROM i.claim_token AND NOT EXISTS(SELECT 1 FROM budget_model_batches b JOIN budget_actions origin ON origin.id=(convert_from(b.manifest,'UTF8')::jsonb #>> '{Slots,0,ActionID}') WHERE b.id=j.model_batch_id AND b.group_digest=m.group_digest AND l.job_token=(convert_from(origin.request,'UTF8')::jsonb #>> '{Job,Token}'))) OR l.sending_fence IS NULL OR l.sending_fence<>l.fence))
AND NOT EXISTS(SELECT 1 FROM budget_actions a WHERE a.attempt_id=j.attempt_id AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=j.run_id
AND (a.state<>'result_recorded' OR NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs done WHERE done.action_id=a.id)
OR (j.recovery_action_id IS NULL AND NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs own WHERE own.action_id=a.id AND own.reduction_id=m.id))))
GROUP BY m.id,m.created_at ORDER BY m.created_at,m.id LIMIT $3`, a.cfg.BoundaryID, a.cfg.ProjectID, limit)
	if err != nil {
		return 0, err
	}
	var actions []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		actions = append(actions, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	count := 0
	for _, id := range actions {
		if _, err = a.CompleteModelJob(ctx, id); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func checkModelCompletionStorage(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT terminal_reduction_id FROM budget_jobs LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

func (a *Authority) ListModelJobs(ctx context.Context, limit int) ([]review.ModelJobProjection, error) {
	if limit < 1 || limit > 128 {
		return nil, errors.New("invalid terminal listing bound")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT j.run_id FROM budget_jobs j JOIN budget_inbox i USING(run_id) WHERE i.boundary_id=$1 AND i.project_id=$2 AND j.terminal_reduction_id IS NOT NULL ORDER BY j.updated_at DESC,j.run_id LIMIT $3`, a.cfg.BoundaryID, a.cfg.ProjectID, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	var out []review.ModelJobProjection
	size := 0
	for _, id := range ids {
		p, err := a.LookupModelJob(ctx, id)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		size += len(raw)
		if size > 64<<20 {
			return nil, errors.New("terminal listing exceeds artifact byte bound")
		}
		out = append(out, *p)
	}
	return out, nil
}
