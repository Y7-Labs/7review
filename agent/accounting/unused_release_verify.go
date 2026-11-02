package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Y4NN777/7review/agent/review"
)

// This read takes no job/head locks: spending already owns scope locks. Reversing
// that order here would deadlock with cancellation and release maintenance.
func verifyUnusedReleaseTx(ctx context.Context, tx *sql.Tx, id string) (*UnusedReleaseReceipt, error) {
	var raw, quote, upper, actual, outcome, cancelRaw, commandRaw []byte
	var proofDigest, cancelID, releaseState, attemptID, bindingDigest, intentDigest, requestDigest string
	var runID, jobCancel, phase, inboxState, cancelDigest, commandDigest, actorID, commandID string
	var releaseID, providerReceipt, jobToken sql.NullString
	var fenced, settled, incident, active, allowed bool
	var version uint64
	var lease unusedLease
	err := tx.QueryRowContext(ctx, `SELECT r.receipt,r.receipt_digest,r.cancel_command_id,a.state,a.unused_release_id,
a.attempt_id,a.intent_digest,a.request_digest,a.quote,a.upper_amount,a.outcome,a.actual_amount,a.receipt_id,a.fenced,a.settled,a.integrity_incident,t.binding_digest,t.active,
j.run_id,j.cancel_command_id,j.control_version,j.phase,j.dispatch_allowed,b.state,b.claim_token,
c.receipt,c.receipt_digest,c.command,c.command_digest,c.actor_id,c.command_id,
coalesce(l.fence,-1),l.worker_id,l.job_token,l.lease_until,l.sending_fence
FROM budget_unused_releases r JOIN budget_actions a ON a.id=r.action_id
JOIN budget_attempts t ON t.id=a.attempt_id
JOIN budget_operator_commands c ON c.id=r.cancel_command_id AND c.attempt_id=a.attempt_id
JOIN budget_jobs j ON j.run_id=c.run_id AND j.attempt_id=a.attempt_id
JOIN budget_inbox b ON b.run_id=j.run_id AND b.boundary_id=c.boundary_id AND b.project_id=c.project_id
LEFT JOIN budget_action_leases l ON l.action_id=a.id WHERE r.action_id=$1`, id).Scan(&raw, &proofDigest, &cancelID, &releaseState, &releaseID, &attemptID, &intentDigest, &requestDigest, &quote, &upper, &outcome, &actual, &providerReceipt, &fenced, &settled, &incident, &bindingDigest, &active, &runID, &jobCancel, &version, &phase, &allowed, &inboxState, &jobToken, &cancelRaw, &cancelDigest, &commandRaw, &commandDigest, &actorID, &commandID, &lease.Fence, &lease.Worker, &lease.JobToken, &lease.Until, &lease.Sending)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIntegrity
	}
	if err != nil {
		return nil, err
	}
	var r UnusedReleaseReceipt
	var cancel review.CancelAttemptReceipt
	var amount review.ResourceLimitScope
	leaseRaw, _ := json.Marshal(lease)
	if len(raw) > 16<<10 || len(cancelRaw) > 8192 || len(commandRaw) > 16<<10 || hash(raw) != proofDigest || hash(cancelRaw) != cancelDigest || hash(commandRaw) != commandDigest || json.Unmarshal(raw, &r) != nil || json.Unmarshal(cancelRaw, &cancel) != nil || json.Unmarshal(upper, &amount) != nil {
		return nil, ErrIntegrity
	}
	if releaseState != "not_executed" || !releaseID.Valid || releaseID.String != id || !fenced || settled || incident || active || allowed || outcome != nil || actual != nil || providerReceipt.Valid || inboxState != "cancelled" || phase != "finished" || jobToken.Valid || !lease.validUnsent() {
		return nil, ErrIntegrity
	}
	if r.Version != 1 || r.ActionID != id || r.AttemptID != attemptID || r.RunID != runID || r.CancelCommandID != cancelID || cancelID != jobCancel || r.CancelReceiptDigest != cancelDigest || r.IntentDigest != intentDigest || r.RequestDigest != requestDigest || r.BindingDigest != bindingDigest || r.QuoteDigest != hash(quote) || r.UpperDigest != hash(upper) || r.LeaseDigest != hash(leaseRaw) || r.Fence != uint64(lease.Fence) || r.ReleasedAt.IsZero() || r.ControlVersion > version || !sameModelJSON(r.Amount, amount) {
		return nil, ErrIntegrity
	}
	if cancel.SchemaVersion != 1 || cancel.AttemptID != r.AttemptID || cancel.ActorID != actorID || cancel.CommandID != commandID || cancel.CommandDigest != commandDigest || cancel.State != "cancelled" || cancel.Kind != "cancel_attempt" || cancel.ReservationDisposition != "retained" || cancel.CreatedAt.IsZero() || cancel.ControlVersion >= r.ControlVersion {
		return nil, ErrIntegrity
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.kind,s.authority_digest,h.origin_period,u.origin_period FROM budget_holds h JOIN budget_scopes s ON s.id=h.scope_id LEFT JOIN budget_unused_release_scopes u ON u.action_id=h.action_id AND u.scope_id=h.scope_id WHERE h.action_id=$1 ORDER BY CASE s.kind WHEN 'attempt' THEN 1 WHEN 'change' THEN 2 ELSE 3 END,s.id`, id)
	if err != nil {
		return nil, err
	}
	var scopes []UnusedReleaseScope
	for rows.Next() {
		var scope UnusedReleaseScope
		var releasedOrigin sql.NullString
		if err = rows.Scan(&scope.ID, &scope.Kind, &scope.Authority, &scope.Origin, &releasedOrigin); err != nil {
			rows.Close()
			return nil, err
		}
		if !releasedOrigin.Valid || releasedOrigin.String != scope.Origin {
			rows.Close()
			return nil, ErrIntegrity
		}
		scopes = append(scopes, scope)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	var links int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM budget_unused_release_scopes WHERE action_id=$1`, id).Scan(&links); err != nil {
		return nil, err
	}
	if links != 3 || len(scopes) != 3 || !sameModelJSON(scopes, r.Scopes) {
		return nil, ErrIntegrity
	}
	var contradiction bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_quarantine WHERE convert_from(payload,'UTF8')::jsonb->>'ActionID'=$1) OR EXISTS(SELECT 1 FROM budget_action_results WHERE action_id=$1) OR EXISTS(SELECT 1 FROM budget_receipts WHERE action_id=$1) OR EXISTS(SELECT 1 FROM budget_model_reduction_inputs WHERE action_id=$1)`, id).Scan(&contradiction); err != nil {
		return nil, err
	}
	if contradiction {
		return nil, ErrIntegrity
	}
	return &r, nil
}
