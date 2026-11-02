package accounting

import (
	"context"
	"errors"

	"github.com/Y4NN777/7review/agent/orchestrator"
)

// AdvancePendingModelToolContinuations is bounded authority-side work. It only
// materializes checked snapshots and prepares exact children; it performs no
// provider I/O or pipeline replay. Duplicate workers return idempotent receipts.
func (a *Authority) AdvancePendingModelToolContinuations(ctx context.Context, limit int, observer orchestrator.ModelToolObserver, builder orchestrator.ModelToolContinuationBuilder) (int, error) {
	if limit < 1 || limit > 128 || observer == nil || builder == nil {
		return 0, errors.New("invalid tool continuation batch or dependencies")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT m.id,min(x.action_id) FROM budget_model_reductions m JOIN budget_model_reduction_inputs x ON x.reduction_id=m.id JOIN budget_jobs j ON j.run_id=m.run_id JOIN budget_inbox i ON i.run_id=j.run_id JOIN budget_attempts t ON t.id=j.attempt_id
WHERE i.boundary_id=$1 AND i.project_id=$2 AND i.state='running' AND i.deadline>clock_timestamp() AND j.phase='effect_started' AND j.dispatch_allowed AND t.active AND j.attempt_id=m.attempt_id
AND (t.tracking_id IS NULL OR EXISTS(SELECT 1 FROM budget_heads h WHERE h.id=t.tracking_id AND h.current_attempt_id=t.id))
AND m.state='needs_tools' AND convert_from(m.artifact,'UTF8')::jsonb #> '{Routing,Roles,reasoner}' IS NOT NULL
AND NOT EXISTS(SELECT 1 FROM budget_model_tool_children c WHERE c.parent_reduction_id=m.id)
AND (j.recovery_action_id IS NULL OR EXISTS(SELECT 1 FROM budget_model_reduction_inputs own WHERE own.reduction_id=m.id AND own.action_id=j.recovery_action_id))
AND ((EXISTS(SELECT 1 FROM budget_model_tool_observations o WHERE o.parent_reduction_id=m.id AND o.id=j.tool_observation_id AND convert_from(o.receipt,'UTF8')::jsonb->>'OwnerToken'=i.claim_token))
OR (NOT EXISTS(SELECT 1 FROM budget_model_tool_observations o WHERE o.parent_reduction_id=m.id) AND (j.recovery_action_id IS NULL OR EXISTS(SELECT 1 FROM budget_action_leases l WHERE l.action_id=j.recovery_action_id AND l.job_token=i.claim_token AND l.sending_fence=l.fence))))
GROUP BY m.id,j.updated_at,m.created_at ORDER BY j.updated_at,m.created_at,m.id LIMIT $3`, a.cfg.BoundaryID, a.cfg.ProjectID, limit)
	if err != nil {
		return 0, err
	}
	type candidate struct{ parent, action string }
	var ids []candidate
	for rows.Next() {
		var id candidate
		if err = rows.Scan(&id.parent, &id.action); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
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
	for _, id := range ids {
		_, err = a.CommitModelToolObservations(ctx, id.action, observer)
		if err == nil {
			_, err = a.AdvanceModelToolContinuation(ctx, id.action, builder)
		}
		if errors.Is(err, ErrBudgetExhausted) {
			if err = a.deferBudgetToolCandidate(ctx, id.parent); err != nil {
				return count, err
			}
			continue
		}
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// This is only a fairness hint. It changes no state, token, deadline, grant or
// hold. Known budget waits must not monopolize a bounded oldest-first scan or
// stop healthy inbox claims; storage/integrity failures still propagate.
func (a *Authority) deferBudgetToolCandidate(ctx context.Context, parentID string) error {
	tx, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs j SET updated_at=clock_timestamp() FROM budget_inbox i,budget_model_reductions m WHERE j.run_id=i.run_id AND m.run_id=j.run_id AND m.id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND i.state='running' AND i.deadline>clock_timestamp() AND j.phase='effect_started' AND j.dispatch_allowed AND NOT EXISTS(SELECT 1 FROM budget_model_tool_children c WHERE c.parent_reduction_id=m.id) AND (j.recovery_action_id IS NULL OR EXISTS(SELECT 1 FROM budget_model_reduction_inputs x WHERE x.reduction_id=m.id AND x.action_id=j.recovery_action_id))`, parentID, a.cfg.BoundaryID, a.cfg.ProjectID); err != nil {
		return err
	}
	return tx.Commit()
}
