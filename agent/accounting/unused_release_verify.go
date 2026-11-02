package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/Y4NN777/7review/agent/review"
)

// This read takes no job/head locks: spending already owns scope locks. Reversing
// that order here would deadlock with cancellation and release maintenance.
func verifyUnusedReleaseTx(ctx context.Context, tx *sql.Tx, id string) (*UnusedReleaseReceipt, error) {
	var raw, quote, upper, actual, outcome, oldBindingRaw []byte
	var cancelRaw, commandRaw, successorBindingRaw, successorIntakeRaw []byte
	var proofDigest, cause, releaseState, attemptID, bindingDigest, intentDigest, requestDigest string
	var runID, phase, inboxState, oldTracking string
	var releaseID, providerReceipt, jobToken, cancelID, jobCancel sql.NullString
	var releaseSuccessor, releaseIntake, oldSuccessor, oldIntake sql.NullString
	var cancelDigest, commandDigest, actorID, commandID sql.NullString
	var successorBindingDigest, successorTracking, successorIntakeAttempt sql.NullString
	var fenced, settled, incident, active, allowed bool
	var version uint64
	var lease unusedLease
	err := tx.QueryRowContext(ctx, `SELECT r.receipt,r.receipt_digest,r.cause,r.cancel_command_id,r.superseding_attempt_id,r.supersession_intake_id,
a.state,a.unused_release_id,a.attempt_id,a.intent_digest,a.request_digest,a.quote,a.upper_amount,a.outcome,a.actual_amount,a.receipt_id,a.fenced,a.settled,a.integrity_incident,
t.binding,t.binding_digest,t.active,t.tracking_id,t.superseded_by_attempt_id,t.supersession_intake_id,
j.run_id,j.cancel_command_id,j.control_version,j.phase,j.dispatch_allowed,b.state,b.claim_token,
c.receipt,c.receipt_digest,c.command,c.command_digest,c.actor_id,c.command_id,
s.binding,s.binding_digest,s.tracking_id,si.attempt_id,si.receipt,
coalesce(l.fence,-1),l.worker_id,l.job_token,l.lease_until,l.sending_fence
FROM budget_unused_releases r JOIN budget_actions a ON a.id=r.action_id
JOIN budget_attempts t ON t.id=a.attempt_id
JOIN budget_jobs j ON j.attempt_id=a.attempt_id AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=j.run_id
JOIN budget_inbox b ON b.run_id=j.run_id
LEFT JOIN budget_operator_commands c ON c.id=r.cancel_command_id AND c.attempt_id=a.attempt_id AND c.run_id=j.run_id
LEFT JOIN budget_attempts s ON s.id=r.superseding_attempt_id
LEFT JOIN budget_intakes si ON si.id=r.supersession_intake_id
LEFT JOIN budget_action_leases l ON l.action_id=a.id
WHERE r.action_id=$1`, id).Scan(&raw, &proofDigest, &cause, &cancelID, &releaseSuccessor, &releaseIntake,
		&releaseState, &releaseID, &attemptID, &intentDigest, &requestDigest, &quote, &upper, &outcome, &actual, &providerReceipt, &fenced, &settled, &incident,
		&oldBindingRaw, &bindingDigest, &active, &oldTracking, &oldSuccessor, &oldIntake,
		&runID, &jobCancel, &version, &phase, &allowed, &inboxState, &jobToken,
		&cancelRaw, &cancelDigest, &commandRaw, &commandDigest, &actorID, &commandID,
		&successorBindingRaw, &successorBindingDigest, &successorTracking, &successorIntakeAttempt, &successorIntakeRaw,
		&lease.Fence, &lease.Worker, &lease.JobToken, &lease.Until, &lease.Sending)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrIntegrity
	}
	if err != nil {
		return nil, err
	}
	var r UnusedReleaseReceipt
	var amount review.ResourceLimitScope
	leaseRaw, _ := json.Marshal(lease)
	if len(raw) > 16<<10 || len(oldBindingRaw) > 1<<20 || hash(raw) != proofDigest || hash(oldBindingRaw) != bindingDigest || json.Unmarshal(raw, &r) != nil || json.Unmarshal(upper, &amount) != nil {
		return nil, ErrIntegrity
	}
	if releaseState != "not_executed" || !releaseID.Valid || releaseID.String != id || !fenced || settled || incident || active || allowed || outcome != nil || actual != nil || providerReceipt.Valid || phase != "finished" || jobToken.Valid || !lease.validUnsent() {
		return nil, ErrIntegrity
	}
	if r.ActionID != id || r.AttemptID != attemptID || r.RunID != runID || r.IntentDigest != intentDigest || r.RequestDigest != requestDigest || r.BindingDigest != bindingDigest || r.QuoteDigest != hash(quote) || r.UpperDigest != hash(upper) || r.LeaseDigest != hash(leaseRaw) || r.Fence != uint64(lease.Fence) || r.ReleasedAt.IsZero() || r.ControlVersion > version || !sameModelJSON(r.Amount, amount) {
		return nil, ErrIntegrity
	}
	var oldBinding attemptBinding
	if json.Unmarshal(oldBindingRaw, &oldBinding) != nil || oldBinding.Attempt.AttemptID != attemptID || oldBinding.Attempt.Change.RepositoryID == "" {
		return nil, ErrIntegrity
	}
	switch cause {
	case "cancelled":
		if err = verifyCancelledUnusedAuthority(r, cancelID, jobCancel, cancelRaw, cancelDigest, commandRaw, commandDigest, actorID, commandID, inboxState); err != nil {
			return nil, err
		}
	case "superseded":
		if err = verifySupersededUnusedAuthority(r, oldBinding, oldTracking, oldSuccessor, oldIntake, releaseSuccessor, releaseIntake, successorBindingRaw, successorBindingDigest, successorTracking, successorIntakeAttempt, successorIntakeRaw, inboxState); err != nil {
			return nil, err
		}
	default:
		return nil, ErrIntegrity
	}
	if err = verifyUnusedReleaseScopes(ctx, tx, id, r.Scopes); err != nil {
		return nil, err
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

func verifyCancelledUnusedAuthority(r UnusedReleaseReceipt, cancelID, jobCancel sql.NullString, cancelRaw []byte, cancelDigest sql.NullString, commandRaw []byte, commandDigest, actorID, commandID sql.NullString, inboxState string) error {
	var cancel review.CancelAttemptReceipt
	if r.Version != 1 || (r.Cause != "" && r.Cause != "cancelled") || inboxState != "cancelled" || !cancelID.Valid || !jobCancel.Valid || cancelID.String != jobCancel.String || r.CancelCommandID != cancelID.String || !cancelDigest.Valid || r.CancelReceiptDigest != cancelDigest.String || r.SupersedingAttemptID != "" || r.SupersessionIntakeID != "" || r.SupersessionReceiptDigest != "" || len(cancelRaw) > 8192 || len(commandRaw) > 16<<10 || !commandDigest.Valid || !actorID.Valid || !commandID.Valid || hash(cancelRaw) != cancelDigest.String || hash(commandRaw) != commandDigest.String || json.Unmarshal(cancelRaw, &cancel) != nil {
		return ErrIntegrity
	}
	if cancel.SchemaVersion != 1 || cancel.AttemptID != r.AttemptID || cancel.ActorID != actorID.String || cancel.CommandID != commandID.String || cancel.CommandDigest != commandDigest.String || cancel.State != "cancelled" || cancel.Kind != "cancel_attempt" || cancel.ReservationDisposition != "retained" || cancel.CreatedAt.IsZero() || cancel.ControlVersion >= r.ControlVersion {
		return ErrIntegrity
	}
	return nil
}

func verifySupersededUnusedAuthority(r UnusedReleaseReceipt, oldBinding attemptBinding, oldTracking string, oldSuccessor, oldIntake, releaseSuccessor, releaseIntake sql.NullString, successorBindingRaw []byte, successorBindingDigest, successorTracking, successorIntakeAttempt sql.NullString, successorIntakeRaw []byte, inboxState string) error {
	if r.Version != 2 || r.Cause != "superseded" || inboxState != "superseded" || r.CancelCommandID != "" || r.CancelReceiptDigest != "" || !oldSuccessor.Valid || !oldIntake.Valid || !releaseSuccessor.Valid || !releaseIntake.Valid || oldSuccessor.String != releaseSuccessor.String || oldIntake.String != releaseIntake.String || r.SupersedingAttemptID != releaseSuccessor.String || r.SupersessionIntakeID != releaseIntake.String || !successorBindingDigest.Valid || !successorTracking.Valid || !successorIntakeAttempt.Valid || successorIntakeAttempt.String != releaseSuccessor.String || successorTracking.String != oldTracking || len(successorBindingRaw) > 1<<20 || len(successorIntakeRaw) > 16<<20 || hash(successorBindingRaw) != successorBindingDigest.String || hash(successorIntakeRaw) != r.SupersessionReceiptDigest {
		return ErrIntegrity
	}
	var successor attemptBinding
	var receipt review.IntakeReceipt
	if json.Unmarshal(successorBindingRaw, &successor) != nil || json.Unmarshal(successorIntakeRaw, &receipt) != nil || successor.ProjectID != oldBinding.ProjectID || successor.Attempt.AttemptID != releaseSuccessor.String || successor.Attempt.PredecessorAttemptID != r.AttemptID || successor.Attempt.Generation != oldBinding.Attempt.Generation+1 || successor.Attempt.Change != oldBinding.Attempt.Change || receipt.Duplicate || !reflect.DeepEqual(receipt.Attempt, successor.Attempt) || !reflect.DeepEqual(receipt.Execution, successor.Execution) || successor.Attestation == nil || !reflect.DeepEqual(receipt.Attestation, *successor.Attestation) || !reflect.DeepEqual(receipt.Policy.Limits, successor.PolicyLimits) || !reflect.DeepEqual(receipt.Policy.OperatorCeilings, &successor.OperatorCeilings) {
		return ErrIntegrity
	}
	return nil
}

func verifyUnusedReleaseScopes(ctx context.Context, tx *sql.Tx, id string, expected []UnusedReleaseScope) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.id,s.kind,s.authority_digest,h.origin_period,u.origin_period FROM budget_holds h JOIN budget_scopes s ON s.id=h.scope_id LEFT JOIN budget_unused_release_scopes u ON u.action_id=h.action_id AND u.scope_id=h.scope_id WHERE h.action_id=$1 ORDER BY CASE s.kind WHEN 'attempt' THEN 1 WHEN 'change' THEN 2 ELSE 3 END,s.id`, id)
	if err != nil {
		return err
	}
	var scopes []UnusedReleaseScope
	for rows.Next() {
		var scope UnusedReleaseScope
		var releasedOrigin sql.NullString
		if err = rows.Scan(&scope.ID, &scope.Kind, &scope.Authority, &scope.Origin, &releasedOrigin); err != nil {
			rows.Close()
			return err
		}
		if !releasedOrigin.Valid || releasedOrigin.String != scope.Origin {
			rows.Close()
			return ErrIntegrity
		}
		scopes = append(scopes, scope)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	var links int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM budget_unused_release_scopes WHERE action_id=$1`, id).Scan(&links); err != nil {
		return err
	}
	if links != 3 || len(scopes) != 3 || !sameModelJSON(scopes, expected) {
		return ErrIntegrity
	}
	return nil
}
