package accounting

import (
	"context"
	"errors"
	"math"

	"github.com/Y4NN777/7review/agent/orchestrator"
)

// DeferPreparedBudgetWait only rotates a scheduling hint after a known pre-send
// capacity denial. It never clears a lease, grant, sending marker or reservation.
func (a *Authority) DeferPreparedBudgetWait(ctx context.Context, p orchestrator.PreparedAction) error {
	if !validID(p.ID) || !canonicalDigest(p.RequestDigest) || p.Fence > math.MaxInt64 || p.AlreadyStarted {
		return errors.New("invalid prepared budget wait")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs j SET updated_at=clock_timestamp() FROM budget_inbox i,budget_actions a,budget_action_leases l WHERE j.run_id=i.run_id AND j.attempt_id=a.attempt_id AND l.action_id=a.id AND a.id=$1 AND a.request_digest=$2 AND l.fence=$3 AND a.fenced AND a.state='prepared' AND l.sending_fence IS NULL AND i.boundary_id=$4 AND i.project_id=$5 AND i.state='running' AND i.deadline>clock_timestamp() AND j.dispatch_allowed AND j.phase IN ('admitted','effect_started') AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=j.run_id`, p.ID, p.RequestDigest, int64(p.Fence), a.cfg.BoundaryID, a.cfg.ProjectID); err != nil {
		return err
	}
	return tx.Commit()
}
