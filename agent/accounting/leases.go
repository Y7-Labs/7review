package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func (a *Authority) storedAction(ctx context.Context, tx *sql.Tx, id string) (orchestrator.DispatchIntent, Quote, error) {
	var i orchestrator.DispatchIntent
	var q Quote
	var raw, quote []byte
	var digest, requestDigest string
	err := tx.QueryRowContext(ctx, `SELECT request,intent_digest,request_digest,quote FROM budget_actions WHERE id=$1`, id).Scan(&raw, &digest, &requestDigest, &quote)
	if err != nil {
		return i, q, err
	}
	if hash(raw) != digest {
		return i, q, errors.New("immutable action integrity failure")
	}
	if err = json.Unmarshal(raw, &i); err != nil {
		return i, q, err
	}
	if err = json.Unmarshal(quote, &q); err != nil {
		return i, q, err
	}
	if i.ActionID != id || i.RequestDigest != requestDigest || hash(i.Payload) != requestDigest || q.RequestDigest != requestDigest {
		return i, q, errors.New("stored action identity mismatch")
	}
	return i, q, nil
}

// The same job -> head -> attempt -> scopes -> action order is used by sending.
func (a *Authority) lockPreparedAuthority(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) error {
	binding, err := a.binding(i)
	if err != nil {
		return err
	}
	if err = a.guardJobDispatch(ctx, tx, i); err != nil {
		return err
	}
	var track sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT tracking_id FROM budget_attempts WHERE id=$1`, i.Attempt.AttemptID).Scan(&track); err != nil {
		return err
	}
	if track.Valid {
		var current sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT current_attempt_id FROM budget_heads WHERE id=$1 FOR UPDATE`, track.String).Scan(&current); err != nil {
			return err
		}
		if current.String != i.Attempt.AttemptID {
			return errors.New("attempt is no longer current")
		}
	}
	var digest string
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT binding_digest,active FROM budget_attempts WHERE id=$1 FOR UPDATE`, i.Attempt.AttemptID).Scan(&digest, &active); err != nil {
		return err
	}
	if !active || digest != hash(binding) {
		return errors.New("attempt closed or authority changed")
	}
	return lockActionScopes(ctx, tx, i.ActionID)
}

func (a *Authority) checkPreparedQuote(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent, q Quote, now time.Time) error {
	if !now.Before(i.Execution.DeadlineAt) {
		return errors.New("prepared execution deadline expired")
	}
	current, err := a.cfg.Catalog.Quote(i, now)
	if err != nil {
		return err
	}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		return err
	}
	storedJSON, err := json.Marshal(q)
	if err != nil {
		return err
	}
	if !bytes.Equal(currentJSON, storedJSON) {
		return errors.New("prepared route/price qualification changed")
	}
	routeID, _ := json.Marshal([]string{q.Qualification.Provider, q.Qualification.Model, q.RouteVersion})
	var routeDigest string
	var qualification []byte
	if err = tx.QueryRowContext(ctx, `SELECT qualification_digest,qualification FROM budget_routes WHERE id=$1 FOR SHARE`, hash(routeID)).Scan(&routeDigest, &qualification); err != nil {
		return err
	}
	if routeDigest != q.QualificationDigest || hash(qualification) != routeDigest {
		return errors.New("prepared route registry integrity failure")
	}
	binding, err := a.binding(i)
	if err != nil {
		return err
	}
	for _, ref := range a.scopeRefs(i, binding) {
		var authority string
		if err = tx.QueryRowContext(ctx, `SELECT authority_digest FROM budget_scopes WHERE id=$1`, ref.id).Scan(&authority); err != nil {
			return err
		}
		if authority != ref.authority {
			return errors.New("prepared scope authority changed")
		}
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_holds WHERE action_id=$1 AND scope_id=$2)`, i.ActionID, ref.id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("prepared reservation is incomplete")
		}
		if err = checkCapacity(ctx, tx, ref, review.ResourceLimitScope{}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Authority) ClaimPrepared(ctx context.Context, p orchestrator.PreparedAction, worker string, lease time.Duration) (orchestrator.ActionClaim, error) {
	return a.claimPrepared(ctx, p, worker, lease, false)
}

// ReclaimPrepared is a trusted-host recovery operation, not a public user command.
// It cannot replay a job. Only this immutable, still-prepared action is eligible.
func (a *Authority) ReclaimPrepared(ctx context.Context, p orchestrator.PreparedAction, worker string, lease time.Duration) (orchestrator.ActionClaim, error) {
	return a.claimPrepared(ctx, p, worker, lease, true)
}

func (a *Authority) claimPrepared(ctx context.Context, p orchestrator.PreparedAction, worker string, lease time.Duration, reclaim bool) (orchestrator.ActionClaim, error) {
	var empty orchestrator.ActionClaim
	if !validID(p.ID) || !canonicalDigest(p.RequestDigest) || !validID(worker) || p.Fence >= math.MaxInt64 || lease < time.Millisecond || lease > 5*time.Minute || p.AlreadyStarted {
		return empty, errors.New("invalid prepared claim")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	original, q, err := a.storedAction(ctx, tx, p.ID)
	if err != nil {
		return empty, err
	}
	i := original
	if i.RequestDigest != p.RequestDigest {
		return empty, errors.New("claim request digest mismatch")
	}
	if reclaim && i.Job != nil {
		var token string
		err = tx.QueryRowContext(ctx, `SELECT i.claim_token FROM budget_inbox i JOIN budget_jobs j USING(run_id) WHERE i.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND i.state='running' AND i.deadline>clock_timestamp() AND j.attempt_id=$4 AND j.dispatch_allowed AND j.phase='admitted' AND (j.recovery_action_id IS NULL OR j.recovery_action_id=$5) FOR UPDATE OF i,j`, i.Job.RunID, a.cfg.BoundaryID, a.cfg.ProjectID, i.Attempt.AttemptID, p.ID).Scan(&token)
		if err != nil {
			return empty, errors.New("recovery job authority unavailable")
		}
		i.Job = &review.JobClaim{RunID: i.Job.RunID, Token: token}
	} else if reclaim && a.cfg.RequireServiceJobs {
		return empty, errors.New("ephemeral recovery requires an acknowledged service handoff")
	}
	if err = a.lockPreparedAuthority(ctx, tx, i); err != nil {
		return empty, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM budget_actions WHERE id=$1 FOR UPDATE`, p.ID).Scan(&state); err != nil {
		return empty, err
	}
	if state != "prepared" {
		return empty, errors.New("action is not prepared; reconcile without resending")
	}
	var fence int64
	var until sql.NullTime
	var owner sql.NullString
	var sent sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT fence,lease_until,worker_id,sending_fence FROM budget_action_leases WHERE action_id=$1 FOR UPDATE`, p.ID).Scan(&fence, &until, &owner, &sent); err != nil {
		return empty, err
	}
	if sent.Valid {
		return empty, errors.New("durable sending marker forbids prepared reclaim")
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return empty, err
	}
	if uint64(fence) != p.Fence {
		return empty, errors.New("prepared fencing epoch changed")
	}
	if !reclaim && (fence != 0 || owner.Valid) {
		return empty, errors.New("prepared action already claimed; explicit recovery required")
	}
	if owner.Valid && (!until.Valid || now.Before(until.Time)) {
		return empty, errors.New("prepared lease is still owned")
	}
	if err = a.checkPreparedQuote(ctx, tx, i, q, now); err != nil {
		return empty, err
	}
	if reclaim && i.Job != nil {
		// Any possible external progress needs its own continuation/reconciliation,
		// not a broad takeover of this job's execution authority.
		var external bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_actions WHERE attempt_id=$1 AND state<>'prepared')`, i.Attempt.AttemptID).Scan(&external); err != nil {
			return empty, err
		}
		if external {
			return empty, errors.New("job has external progress; validated continuation required")
		}
		token, e := review.NewOpaqueID("recovered-job-")
		if e != nil {
			return empty, e
		}
		i.Job = &review.JobClaim{RunID: i.Job.RunID, Token: token}
		if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET claim_token=$2,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID, token); err != nil {
			return empty, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET recovery_action_id=$2,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID, p.ID); err != nil {
			return empty, err
		}
	}
	deadline := now.Add(lease).UTC().Truncate(time.Microsecond)
	for _, cap := range []time.Time{i.Execution.DeadlineAt, q.Qualification.ValidUntil} {
		if cap.Before(deadline) {
			deadline = cap
		}
	}
	if !now.Before(deadline) {
		return empty, errors.New("prepared lease has no remaining window")
	}
	var token any
	if i.Job != nil {
		token = i.Job.Token
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_action_leases SET worker_id=$2,fence=fence+1,lease_until=$3,job_token=$4 WHERE action_id=$1`, p.ID, worker, deadline, token); err != nil {
		return empty, err
	}
	claim := orchestrator.ActionClaim{ID: p.ID, RequestDigest: p.RequestDigest, WorkerID: worker, Fence: uint64(fence) + 1, LeaseUntil: deadline, Job: i.Job, Intent: &original}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return claim, nil
}

func (a *Authority) BeginPrepared(ctx context.Context, c orchestrator.ActionClaim) (orchestrator.DispatchPermit, error) {
	var empty orchestrator.DispatchPermit
	if !validID(c.ID) || !validID(c.WorkerID) || !canonicalDigest(c.RequestDigest) || c.Fence == 0 || c.Fence > math.MaxInt64 {
		return empty, errors.New("invalid sending claim")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	i, q, err := a.storedAction(ctx, tx, c.ID)
	if err != nil {
		return empty, err
	}
	if i.Job != nil {
		if c.Job == nil || c.Job.RunID != i.Job.RunID {
			return empty, errors.New("sending job claim mismatch")
		}
		i.Job = c.Job
	} else if c.Job != nil {
		return empty, errors.New("unexpected sending job")
	}
	if err = a.lockPreparedAuthority(ctx, tx, i); err != nil {
		return empty, err
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM budget_actions WHERE id=$1 FOR UPDATE`, c.ID).Scan(&state); err != nil {
		return empty, err
	}
	var fence int64
	var worker sql.NullString
	var until sql.NullTime
	var token sql.NullString
	var sent sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT fence,worker_id,lease_until,job_token,sending_fence FROM budget_action_leases WHERE action_id=$1 FOR UPDATE`, c.ID).Scan(&fence, &worker, &until, &token, &sent); err != nil {
		return empty, err
	}
	if uint64(fence) != c.Fence || worker.String != c.WorkerID || !until.Valid || !until.Time.Equal(c.LeaseUntil) || c.RequestDigest != i.RequestDigest || (i.Job != nil && token.String != i.Job.Token) {
		return empty, errors.New("sending claim is stale or mismatched")
	}
	notAfter := i.Execution.DeadlineAt
	if q.Qualification.ValidUntil.Before(notAfter) {
		notAfter = q.Qualification.ValidUntil
	}
	permit := orchestrator.DispatchPermit{ID: c.ID, RequestDigest: c.RequestDigest, Fence: c.Fence, WorkerID: c.WorkerID, ActiveMS: q.ActiveMS, NotAfter: notAfter}
	if state != "prepared" {
		if !sent.Valid || sent.Int64 != fence {
			return empty, errors.New("sending fence is inconsistent")
		}
		permit.AlreadyStarted = true
		return permit, nil
	}
	if sent.Valid {
		return empty, errors.New("prepared state conflicts with durable sending fence")
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return empty, err
	}
	if !now.Before(until.Time) {
		return empty, errors.New("sending lease expired")
	}
	if err = a.checkPreparedQuote(ctx, tx, i, q, now); err != nil {
		return empty, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE budget_action_leases SET sending_fence=fence WHERE action_id=$1 AND lease_until>clock_timestamp()`, c.ID)
	if err != nil {
		return empty, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return empty, errors.New("sending lease expired before marker")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_actions SET state='sending' WHERE id=$1`, c.ID); err != nil {
		return empty, err
	}
	if i.Job != nil {
		if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET phase='effect_started',updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID); err != nil {
			return empty, err
		}
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return permit, nil
}

func validateOutcomeFence(ctx context.Context, tx *sql.Tx, p orchestrator.DispatchPermit) error {
	var fenced bool
	if err := tx.QueryRowContext(ctx, `SELECT fenced FROM budget_actions WHERE id=$1`, p.ID).Scan(&fenced); err != nil {
		return err
	}
	if !fenced {
		if p.Fence != 0 || p.WorkerID != "" {
			return errors.New("unexpected outcome fencing credentials")
		}
		return nil
	}
	var fence sql.NullInt64
	var worker sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT sending_fence,worker_id FROM budget_action_leases WHERE action_id=$1 FOR SHARE`, p.ID).Scan(&fence, &worker); err != nil {
		return err
	}
	if !fence.Valid || fence.Int64 <= 0 || uint64(fence.Int64) != p.Fence || worker.String != p.WorkerID {
		return errors.New("outcome was not issued to the sending owner")
	}
	return nil
}

// PreparedRecoveryCandidates discovers durable work after a lost preparation
// response. The descriptor is not permission to send; claim/begin recheck it.
func (a *Authority) PreparedRecoveryCandidates(ctx context.Context, limit int) ([]orchestrator.PreparedAction, error) {
	if limit < 1 || limit > 128 {
		return nil, errors.New("invalid prepared recovery batch bound")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.request_digest,l.fence FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id JOIN budget_jobs j ON j.attempt_id=a.attempt_id JOIN budget_inbox i ON i.run_id=j.run_id JOIN budget_attempts t ON t.id=a.attempt_id WHERE i.boundary_id=$1 AND i.project_id=$2 AND i.state='running' AND i.deadline>clock_timestamp() AND j.phase='admitted' AND j.dispatch_allowed AND t.active AND a.state='prepared' AND a.fenced AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=j.run_id AND (l.lease_until IS NULL OR l.lease_until<=clock_timestamp()) AND (t.tracking_id IS NULL OR EXISTS(SELECT 1 FROM budget_heads h WHERE h.id=t.tracking_id AND h.current_attempt_id=t.id)) AND NOT EXISTS(SELECT 1 FROM budget_actions e WHERE e.attempt_id=t.id AND e.state<>'prepared') ORDER BY a.created_at,a.id LIMIT $3`, a.cfg.BoundaryID, a.cfg.ProjectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []orchestrator.PreparedAction
	for rows.Next() {
		var p orchestrator.PreparedAction
		var fence int64
		if err = rows.Scan(&p.ID, &p.RequestDigest, &fence); err != nil {
			return nil, err
		}
		p.Fence = uint64(fence)
		candidates = append(candidates, p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return candidates, nil
}
