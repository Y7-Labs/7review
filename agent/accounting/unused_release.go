package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

var ErrUnusedReleaseUnavailable = errors.New("no verified unused reservation disposition")

type UnusedReleaseScope struct{ ID, Kind, Authority, Origin string }

// UnusedReleaseReceipt proves local non-execution, not provider-reported usage.
// Its scope links preserve reservation provenance; budget reads require the
// proof before treating any original reservation amount as unused.
type UnusedReleaseReceipt struct {
	Version                                                                           int
	Cause                                                                             string
	ActionID, AttemptID, RunID, CancelCommandID, CancelReceiptDigest                  string
	SupersedingAttemptID, SupersessionIntakeID, SupersessionReceiptDigest             string
	IntentDigest, RequestDigest, QuoteDigest, UpperDigest, BindingDigest, LeaseDigest string
	Fence, ControlVersion                                                             uint64
	ReleasedAt                                                                        time.Time
	Amount                                                                            review.ResourceLimitScope
	Scopes                                                                            []UnusedReleaseScope
}

type unusedLease struct {
	Fence            int64
	Worker, JobToken sql.NullString
	Until            sql.NullTime
	Sending          sql.NullInt64
}

func (l unusedLease) validUnsent() bool {
	if l.Fence < 0 || l.Sending.Valid {
		return false
	}
	if l.Fence == 0 {
		return !l.Worker.Valid && !l.JobToken.Valid && !l.Until.Valid
	}
	return l.Worker.Valid && validID(l.Worker.String) && l.JobToken.Valid && validID(l.JobToken.String) && l.Until.Valid && !l.Until.Time.IsZero()
}

func checkUnusedReleaseStorage(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT a.unused_release_id,r.action_id,r.cause,r.cancel_command_id,r.superseding_attempt_id,r.supersession_intake_id,r.receipt,r.receipt_digest,s.scope_id,s.origin_period FROM budget_actions a LEFT JOIN budget_unused_releases r ON r.action_id=a.unused_release_id LEFT JOIN budget_unused_release_scopes s ON s.action_id=r.action_id LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

// RecoverUnusedPrepared verifies bounded cancellation and supersession
// dispositions. It never dispatches an action or replays a job.
func (a *Authority) RecoverUnusedPrepared(ctx context.Context, limit int) (int, error) {
	return a.recoverUnusedPrepared(ctx, limit, false)
}

// RecoverCancelledPrepared verifies a bounded set of release receipts. A count
// is processed evidence, not new credit; concurrent workers may see the same ID.
func (a *Authority) RecoverCancelledPrepared(ctx context.Context, limit int) (int, error) {
	return a.recoverUnusedPrepared(ctx, limit, true)
}

func (a *Authority) recoverUnusedPrepared(ctx context.Context, limit int, cancelledOnly bool) (int, error) {
	if limit < 1 || limit > 128 {
		return 0, errors.New("invalid unused-release recovery bound")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = checkUnusedReleaseStorage(ctx, tx); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id FROM budget_actions a JOIN budget_jobs j ON j.attempt_id=a.attempt_id JOIN budget_inbox b ON b.run_id=j.run_id JOIN budget_attempts t ON t.id=a.attempt_id WHERE b.boundary_id=$1 AND b.project_id=$2 AND NOT t.active AND a.state='prepared' AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=j.run_id AND ((b.state='cancelled' AND j.cancel_command_id IS NOT NULL AND NOT j.dispatch_allowed AND j.phase='finished') OR (NOT $3 AND j.cancel_command_id IS NULL AND t.superseded_by_attempt_id IS NOT NULL AND t.supersession_intake_id IS NOT NULL)) ORDER BY j.updated_at,a.created_at,a.id LIMIT $4`, a.cfg.BoundaryID, a.cfg.ProjectID, cancelledOnly, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		if _, err = a.ReleaseUnusedPrepared(ctx, id); err != nil {
			if errors.Is(err, ErrUnusedReleaseUnavailable) {
				continue
			}
			return count, err
		}
		count++
	}
	return count, nil
}

// ReleaseUnusedPrepared is trusted maintenance over an already durable
// cancellation or exact successor intake. It cannot create either authority.
func (a *Authority) ReleaseUnusedPrepared(ctx context.Context, id string) (*UnusedReleaseReceipt, error) {
	return a.releaseUnusedPrepared(ctx, id, false)
}

// ReleaseCancelledPrepared is trusted maintenance over an already authenticated
// cancellation. It cannot cancel, resume or send work, or infer zero from expiry.
func (a *Authority) ReleaseCancelledPrepared(ctx context.Context, id string) (*UnusedReleaseReceipt, error) {
	return a.releaseUnusedPrepared(ctx, id, true)
}

func (a *Authority) releaseUnusedPrepared(ctx context.Context, id string, cancelledOnly bool) (*UnusedReleaseReceipt, error) {
	if !validID(id) {
		return nil, ErrUnusedReleaseUnavailable
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	i, q, err := a.storedAction(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if i.Job == nil {
		return nil, ErrUnusedReleaseUnavailable
	}
	var cancelID sql.NullString
	var version uint64
	var inboxState, phase string
	var claimToken sql.NullString
	var dispatchAllowed bool
	err = tx.QueryRowContext(ctx, `SELECT j.cancel_command_id,j.control_version,b.state,b.claim_token,j.phase,j.dispatch_allowed FROM budget_inbox b JOIN budget_jobs j ON j.run_id=b.run_id WHERE b.run_id=$1 AND b.boundary_id=$2 AND b.project_id=$3 AND j.attempt_id=$4 FOR UPDATE OF b,j`, i.Job.RunID, a.cfg.BoundaryID, a.cfg.ProjectID, i.Attempt.AttemptID).Scan(&cancelID, &version, &inboxState, &claimToken, &phase, &dispatchAllowed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnusedReleaseUnavailable
	}
	if err != nil {
		return nil, err
	}
	historical := *a
	historical.cfg.Limits = i.OperatorCeilings.Clone()
	binding, err := historical.binding(i)
	if err != nil {
		return nil, err
	}
	aggregate, err := a.readAggregateTx(ctx, tx, i.Job.RunID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(aggregate.Receipt.Attempt, i.Attempt) || !reflect.DeepEqual(aggregate.Receipt.Execution, i.Execution) {
		return nil, ErrIntegrity
	}
	var tracking, supersedingAttempt, supersessionIntake sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT tracking_id,superseded_by_attempt_id,supersession_intake_id FROM budget_attempts WHERE id=$1`, i.Attempt.AttemptID).Scan(&tracking, &supersedingAttempt, &supersessionIntake); err != nil || !tracking.Valid {
		return nil, ErrIntegrity
	}
	// Historical cleanup may outlive currentness, but shares dispatch lock order.
	var head sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT current_attempt_id FROM budget_heads WHERE id=$1 FOR UPDATE`, tracking.String).Scan(&head); err != nil {
		return nil, err
	}
	var active bool
	var digest string
	if err = tx.QueryRowContext(ctx, `SELECT active,binding_digest FROM budget_attempts WHERE id=$1 FOR UPDATE`, i.Attempt.AttemptID).Scan(&active, &digest); err != nil {
		return nil, err
	}
	if digest != hash(binding) || !head.Valid {
		return nil, ErrIntegrity
	}
	if !cancelID.Valid && (!supersedingAttempt.Valid || !supersessionIntake.Valid) {
		return nil, ErrUnusedReleaseUnavailable
	}
	if active {
		if !cancelID.Valid {
			return nil, ErrUnusedReleaseUnavailable
		}
		return nil, ErrIntegrity
	}
	authority := UnusedReleaseReceipt{Cause: "cancelled", CancelCommandID: cancelID.String}
	if cancelID.Valid {
		if inboxState != "cancelled" || claimToken.Valid || phase != "finished" || dispatchAllowed {
			return nil, ErrIntegrity
		}
		authority.CancelReceiptDigest, err = a.cancelAuditForUnusedTx(ctx, tx, cancelID.String, i, version)
	} else {
		if cancelledOnly {
			return nil, ErrUnusedReleaseUnavailable
		}
		if head.String == i.Attempt.AttemptID {
			return nil, ErrIntegrity
		}
		authority.Cause = "superseded"
		authority.Version = 2
		authority.SupersedingAttemptID = supersedingAttempt.String
		authority.SupersessionIntakeID = supersessionIntake.String
		authority.SupersessionReceiptDigest, err = a.supersessionAuditForUnusedTx(ctx, tx, i, tracking.String, supersedingAttempt.String, supersessionIntake.String)
	}
	if err != nil {
		return nil, err
	}
	refs := historical.scopeRefs(i, binding)
	for _, ref := range refs {
		var authority, kind string
		if err = tx.QueryRowContext(ctx, `SELECT authority_digest,kind FROM budget_scopes WHERE id=$1 FOR UPDATE`, ref.id).Scan(&authority, &kind); err != nil {
			return nil, err
		}
		if authority != ref.authority || kind != ref.kind {
			return nil, ErrIntegrity
		}
	}
	var state string
	var fenced, settled, incident bool
	var outcome, actual, quoteRaw, upperRaw []byte
	var receiptID, releaseID sql.NullString
	var intentDigest, requestDigest string
	if err = tx.QueryRowContext(ctx, `SELECT state,fenced,settled,integrity_incident,outcome,actual_amount,receipt_id,unused_release_id,quote,upper_amount,intent_digest,request_digest FROM budget_actions WHERE id=$1 FOR UPDATE`, id).Scan(&state, &fenced, &settled, &incident, &outcome, &actual, &receiptID, &releaseID, &quoteRaw, &upperRaw, &intentDigest, &requestDigest); err != nil {
		return nil, err
	}
	if state != "prepared" && state != "not_executed" {
		return nil, ErrUnusedReleaseUnavailable
	}
	if !fenced || settled || incident || outcome != nil || actual != nil || receiptID.Valid {
		return nil, ErrIntegrity
	}
	var lease unusedLease
	if err = tx.QueryRowContext(ctx, `SELECT fence,worker_id,job_token,lease_until,sending_fence FROM budget_action_leases WHERE action_id=$1 FOR UPDATE`, id).Scan(&lease.Fence, &lease.Worker, &lease.JobToken, &lease.Until, &lease.Sending); err != nil {
		return nil, err
	}
	if !lease.validUnsent() {
		return nil, ErrIntegrity
	}
	var quarantined bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_quarantine WHERE convert_from(payload,'UTF8')::jsonb->>'ActionID'=$1) OR EXISTS(SELECT 1 FROM budget_action_results WHERE action_id=$1) OR EXISTS(SELECT 1 FROM budget_receipts WHERE action_id=$1) OR EXISTS(SELECT 1 FROM budget_model_reduction_inputs WHERE action_id=$1)`, id).Scan(&quarantined); err != nil {
		return nil, err
	}
	if quarantined {
		return nil, ErrIntegrity
	}
	// Recheck immutable action/checkpoint bytes after all mutation locks.
	lockedIntent, lockedQuote, err := a.storedAction(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !sameModelJSON(i, lockedIntent) || !sameModelJSON(q, lockedQuote) || requestDigest != i.RequestDigest {
		return nil, ErrIntegrity
	}
	// Qualification is frozen evidence, not a fresh provider/price authorization.
	frozen, err := NewCatalog([]RouteBound{q.Qualification})
	if err != nil {
		return nil, ErrIntegrity
	}
	expectedQuote, err := frozen.Quote(i, q.Qualification.ValidFrom)
	if err != nil || !sameModelJSON(q, expectedQuote) {
		return nil, ErrIntegrity
	}
	routeKey, _ := json.Marshal([]string{q.Qualification.Provider, q.Qualification.Model, q.RouteVersion})
	var routeRaw []byte
	var routeDigest string
	if err = tx.QueryRowContext(ctx, `SELECT qualification,qualification_digest FROM budget_routes WHERE id=$1`, hash(routeKey)).Scan(&routeRaw, &routeDigest); err != nil {
		return nil, err
	}
	if hash(routeRaw) != routeDigest || routeDigest != q.QualificationDigest {
		return nil, ErrIntegrity
	}
	var upper review.ResourceLimitScope
	if json.Unmarshal(upperRaw, &upper) != nil {
		return nil, ErrIntegrity
	}
	expectedUpper := review.ResourceLimitScope{ModelCalls: 1, InputTokens: q.InputTokens, OutputTokens: q.OutputTokens, MoneyMicro: q.MoneyMicro, ActiveMS: q.ActiveMS}
	if !sameModelJSON(upper, expectedUpper) {
		return nil, ErrIntegrity
	}
	leaseRaw, _ := json.Marshal(lease)
	r := authority
	if r.Version == 0 {
		r.Version = 1
	}
	r.ActionID, r.AttemptID, r.RunID = id, i.Attempt.AttemptID, i.Job.RunID
	r.IntentDigest, r.RequestDigest, r.QuoteDigest, r.UpperDigest, r.BindingDigest, r.LeaseDigest = intentDigest, requestDigest, hash(quoteRaw), hash(upperRaw), digest, hash(leaseRaw)
	r.Fence, r.ControlVersion, r.Amount = uint64(lease.Fence), version+1, upper
	table := "budget_holds"
	if state == "not_executed" {
		table = "budget_unused_release_scopes"
	}
	for _, ref := range refs {
		var origin string
		if err = tx.QueryRowContext(ctx, `SELECT origin_period FROM `+table+` WHERE action_id=$1 AND scope_id=$2`, id, ref.id).Scan(&origin); err != nil {
			return nil, err
		}
		if origin == "" || (ref.kind == "attempt" && origin != "lifetime") {
			return nil, ErrIntegrity
		}
		if ref.kind != "attempt" {
			n, err := strconv.ParseInt(origin, 10, 64)
			if err != nil || n < 0 || strconv.FormatInt(n, 10) != origin {
				return nil, ErrIntegrity
			}
		}
		r.Scopes = append(r.Scopes, UnusedReleaseScope{ref.id, ref.kind, ref.authority, origin})
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE action_id=$1`, id).Scan(&count); err != nil || count != 3 {
		return nil, ErrIntegrity
	}
	if state == "not_executed" {
		var raw []byte
		var proofDigest, proofCause string
		var proofCancel, proofSuccessor, proofIntake sql.NullString
		var prior UnusedReleaseReceipt
		if err = tx.QueryRowContext(ctx, `SELECT receipt,receipt_digest,cause,cancel_command_id,superseding_attempt_id,supersession_intake_id FROM budget_unused_releases WHERE action_id=$1`, id).Scan(&raw, &proofDigest, &proofCause, &proofCancel, &proofSuccessor, &proofIntake); err != nil {
			return nil, err
		}
		if len(raw) > 16<<10 || hash(raw) != proofDigest || json.Unmarshal(raw, &prior) != nil || !releaseID.Valid || releaseID.String != id || proofCause != r.Cause || proofCancel.String != r.CancelCommandID || proofSuccessor.String != r.SupersedingAttemptID || proofIntake.String != r.SupersessionIntakeID || prior.ReleasedAt.IsZero() || prior.ControlVersion > version {
			return nil, ErrIntegrity
		}
		r.ReleasedAt = prior.ReleasedAt
		r.ControlVersion = prior.ControlVersion
		// V17 cancellation receipts predate the explicit cause field. The v18
		// migration preserves their immutable bytes and records the cause in SQL.
		if prior.Version == 1 && prior.Cause == "" && proofCause == "cancelled" {
			r.Cause = ""
		}
		if !sameModelJSON(r, prior) {
			return nil, ErrIntegrity
		}
		if _, err = verifyUnusedReleaseTx(ctx, tx, id); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return &prior, nil
	}
	if releaseID.Valid {
		return nil, ErrIntegrity
	}
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&r.ReleasedAt); err != nil {
		return nil, err
	}
	r.ReleasedAt = r.ReleasedAt.UTC()
	raw, _ := json.Marshal(r)
	if r.Cause == "superseded" {
		if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET phase='finished',dispatch_allowed=false,control_version=control_version+1,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET state='superseded',claim_token=NULL,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_unused_releases(action_id,cause,cancel_command_id,superseding_attempt_id,supersession_intake_id,receipt,receipt_digest) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, r.Cause, nullableString(r.CancelCommandID), nullableString(r.SupersedingAttemptID), nullableString(r.SupersessionIntakeID), raw, hash(raw)); err != nil {
		return nil, err
	}
	for _, scope := range r.Scopes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_unused_release_scopes(action_id,scope_id,origin_period) VALUES($1,$2,$3)`, id, scope.ID, scope.Origin); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_actions SET state='not_executed',unused_release_id=id WHERE id=$1`, id); err != nil {
		return nil, err
	}
	if r.Cause == "cancelled" {
		_, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET control_version=control_version+1,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID)
	}
	if err != nil {
		return nil, err
	}
	if _, err = verifyUnusedReleaseTx(ctx, tx, id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (a *Authority) cancelAuditForUnusedTx(ctx context.Context, tx *sql.Tx, id string, i orchestrator.DispatchIntent, version uint64) (string, error) {
	var raw, receiptRaw []byte
	var commandDigest, receiptDigest, commandID, actorID string
	if err := tx.QueryRowContext(ctx, `SELECT command,command_digest,receipt,receipt_digest,command_id,actor_id FROM budget_operator_commands WHERE id=$1 AND boundary_id=$2 AND project_id=$3 AND attempt_id=$4 AND run_id=$5`, id, a.cfg.BoundaryID, a.cfg.ProjectID, i.Attempt.AttemptID, i.Job.RunID).Scan(&raw, &commandDigest, &receiptRaw, &receiptDigest, &commandID, &actorID); err != nil {
		return "", err
	}
	var envelope struct {
		BoundaryID, ProjectID, ActorID string
		Command                        review.CancelAttemptCommand
	}
	var r review.CancelAttemptReceipt
	if len(raw) > 16<<10 || len(receiptRaw) > 8192 || hash(raw) != commandDigest || hash(receiptRaw) != receiptDigest || json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(receiptRaw, &r) != nil {
		return "", ErrIntegrity
	}
	c := envelope.Command
	keyRaw, _ := json.Marshal([]string{a.cfg.BoundaryID, a.cfg.ProjectID, c.CommandID})
	if c.Validate() != nil || hash(keyRaw) != id || envelope.BoundaryID != a.cfg.BoundaryID || envelope.ProjectID != a.cfg.ProjectID || envelope.ActorID != actorID || c.CommandID != commandID || c.AttemptID != i.Attempt.AttemptID || c.Revision != i.Attempt.Snapshot.AnalysisRevision() || r.SchemaVersion != 1 || r.CommandID != c.CommandID || r.CommandDigest != commandDigest || r.ActorID != actorID || r.Kind != "cancel_attempt" || r.State != "cancelled" || r.ReservationDisposition != "retained" || r.AttemptID != c.AttemptID || r.Revision != c.Revision || r.ControlVersion != c.ExpectedVersion+1 || r.ControlVersion > version || r.CreatedAt.IsZero() {
		return "", ErrIntegrity
	}
	return receiptDigest, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (a *Authority) supersessionAuditForUnusedTx(ctx context.Context, tx *sql.Tx, old orchestrator.DispatchIntent, trackingID, successorID, intakeID string) (string, error) {
	var bindingRaw, intakeRaw []byte
	var bindingDigest, storedTracking, intakeAttempt string
	err := tx.QueryRowContext(ctx, `SELECT s.binding,s.binding_digest,s.tracking_id,i.attempt_id,i.receipt FROM budget_attempts s JOIN budget_intakes i ON i.id=$2 WHERE s.id=$1`, successorID, intakeID).Scan(&bindingRaw, &bindingDigest, &storedTracking, &intakeAttempt, &intakeRaw)
	if err != nil {
		return "", err
	}
	var successor attemptBinding
	var receipt review.IntakeReceipt
	if len(bindingRaw) > 1<<20 || len(intakeRaw) > 16<<20 || hash(bindingRaw) != bindingDigest || json.Unmarshal(bindingRaw, &successor) != nil || json.Unmarshal(intakeRaw, &receipt) != nil {
		return "", ErrIntegrity
	}
	if storedTracking != trackingID || intakeAttempt != successorID || successor.ProjectID != a.cfg.ProjectID || successor.Attempt.AttemptID != successorID || successor.Attempt.PredecessorAttemptID != old.Attempt.AttemptID || successor.Attempt.Generation != old.Attempt.Generation+1 || successor.Attempt.Change != old.Attempt.Change || receipt.Duplicate || !reflect.DeepEqual(receipt.Attempt, successor.Attempt) || !reflect.DeepEqual(receipt.Execution, successor.Execution) || successor.Attestation == nil || !reflect.DeepEqual(receipt.Attestation, *successor.Attestation) || !reflect.DeepEqual(receipt.Policy.Limits, successor.PolicyLimits) || !reflect.DeepEqual(receipt.Policy.OperatorCeilings, &successor.OperatorCeilings) {
		return "", ErrIntegrity
	}
	return hash(intakeRaw), nil
}
