package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/Y4NN777/7review/agent/review"
)

var ErrOperatorCommandConflict = errors.New("operator command conflict")

type OperatorCommandConflict struct {
	CurrentAttemptID string `json:"current_attempt_id"`
	ControlVersion   uint64 `json:"control_version"`
	Reason           string `json:"reason"`
}

func (e *OperatorCommandConflict) Error() string { return "operator command conflict: " + e.Reason }
func (e *OperatorCommandConflict) Unwrap() error { return ErrOperatorCommandConflict }

// CancelAttempt is a control disposition, not a usage receipt. It retains all
// reservations and positive sending evidence for later attributable settlement.
func (a *Authority) CancelAttempt(ctx context.Context, actor review.OperatorActor, c review.CancelAttemptCommand) (*review.CancelAttemptReceipt, error) {
	if err := a.cfg.Operators.ValidateActor(actor); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(struct {
		BoundaryID, ProjectID, ActorID string
		Command                        review.CancelAttemptCommand
	}{a.cfg.BoundaryID, a.cfg.ProjectID, actor.PrincipalID(), c})
	keyRaw, _ := json.Marshal([]string{a.cfg.BoundaryID, a.cfg.ProjectID, c.CommandID})
	key, digest := hash(keyRaw), hash(payload)
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return nil, err
	}
	record, err := a.operatorAttemptTx(ctx, tx, actor, c.AttemptID, true)
	if err != nil {
		return nil, err
	}
	v := record.view
	conflict := func(reason, current string) error {
		return &OperatorCommandConflict{CurrentAttemptID: current, ControlVersion: v.ControlVersion, Reason: reason}
	}
	var priorCommand, priorReceipt []byte
	var priorDigest, receiptDigest string
	var priorActor, priorAttempt, priorRun, priorID string
	err = tx.QueryRowContext(ctx, `SELECT command,command_digest,receipt,receipt_digest,actor_id,attempt_id,run_id,command_id FROM budget_operator_commands WHERE id=$1 AND boundary_id=$2 AND project_id=$3`, key, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&priorCommand, &priorDigest, &priorReceipt, &receiptDigest, &priorActor, &priorAttempt, &priorRun, &priorID)
	if err == nil {
		if len(priorCommand) > 16<<10 || len(priorReceipt) > 8192 || hash(priorCommand) != priorDigest || hash(priorReceipt) != receiptDigest {
			return nil, ErrIntegrity
		}
		if priorDigest != digest || !bytes.Equal(priorCommand, payload) {
			return nil, conflict("command ID already used with different actor or payload", c.AttemptID)
		}
		var receipt review.CancelAttemptReceipt
		if json.Unmarshal(priorReceipt, &receipt) != nil || receipt.SchemaVersion != 1 || receipt.CreatedAt.IsZero() || receipt.Revision != c.Revision || receipt.ControlVersion != c.ExpectedVersion+1 || priorActor != receipt.ActorID || priorAttempt != c.AttemptID || priorRun != v.RunID || priorID != c.CommandID || receipt.CommandID != c.CommandID || receipt.CommandDigest != digest || receipt.AttemptID != c.AttemptID || receipt.ActorID != actor.PrincipalID() || receipt.Kind != "cancel_attempt" || receipt.State != "cancelled" || receipt.ReservationDisposition != "retained" || receipt.ControlVersion > v.ControlVersion || !record.cancelID.Valid || record.cancelID.String != key || v.InboxState != "cancelled" || v.JobPhase != "finished" || v.Active {
			return nil, ErrIntegrity
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return &receipt, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if !record.tracking.Valid {
		return nil, ErrIntegrity
	}
	var current sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT current_attempt_id FROM budget_heads WHERE id=$1 FOR UPDATE`, record.tracking.String).Scan(&current); err != nil {
		return nil, err
	}
	if !current.Valid {
		return nil, ErrIntegrity
	}
	if current.String != c.AttemptID {
		var raw []byte
		var stored string
		var b attemptBinding
		if err = tx.QueryRowContext(ctx, `SELECT binding,binding_digest FROM budget_attempts WHERE id=$1`, current.String).Scan(&raw, &stored); err != nil {
			return nil, err
		}
		if len(raw) > 1<<20 || hash(raw) != stored || json.Unmarshal(raw, &b) != nil || b.ProjectID != a.cfg.ProjectID || !reflect.DeepEqual(b.Attempt.Change, record.binding.Attempt.Change) {
			return nil, ErrIntegrity
		}
		if err = tx.QueryRowContext(ctx, `SELECT j.control_version FROM budget_jobs j JOIN budget_inbox i ON i.run_id=j.run_id WHERE j.attempt_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND convert_from(j.receipt,'UTF8')::jsonb->>'Duplicate'='false'`, current.String, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&v.ControlVersion); err != nil {
			return nil, ErrIntegrity
		}
		return nil, conflict("attempt is no longer current", current.String)
	}
	var active bool
	var bindingDigest string
	if err = tx.QueryRowContext(ctx, `SELECT active,binding_digest FROM budget_attempts WHERE id=$1 FOR UPDATE`, c.AttemptID).Scan(&active, &bindingDigest); err != nil {
		return nil, err
	}
	if c.ExpectedVersion != v.ControlVersion || c.Revision != v.Revision {
		return nil, conflict("stale control version or revision", c.AttemptID)
	}
	if !active || record.cancelID.Valid || v.JobPhase == "finished" || v.JobPhase == "legacy_unknown" || (v.InboxState != "pending" && v.InboxState != "running" && v.InboxState != "expired" && v.InboxState != "uncertain") {
		return nil, conflict("attempt is not cancellable", c.AttemptID)
	}
	if record.bindingDigest != bindingDigest {
		return nil, ErrIntegrity
	}
	receipt := review.CancelAttemptReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: digest, AttemptID: c.AttemptID, ActorID: actor.PrincipalID(), Kind: "cancel_attempt", Revision: c.Revision, ControlVersion: v.ControlVersion + 1, State: "cancelled", ReservationDisposition: "retained"}
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&receipt.CreatedAt); err != nil {
		return nil, err
	}
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	raw, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_operator_commands(id,boundary_id,project_id,command_id,actor_id,attempt_id,run_id,command,command_digest,receipt,receipt_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, key, a.cfg.BoundaryID, a.cfg.ProjectID, c.CommandID, actor.PrincipalID(), c.AttemptID, v.RunID, payload, digest, raw, hash(raw)); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET phase='finished',dispatch_allowed=false,cancel_command_id=$2,control_version=control_version+1,updated_at=clock_timestamp() WHERE run_id=$1`, v.RunID, key); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET state='cancelled',claim_token=NULL,updated_at=clock_timestamp() WHERE run_id=$1`, v.RunID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_attempts SET active=false WHERE id=$1`, c.AttemptID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func checkOperatorStorage(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT j.control_version,j.cancel_command_id,c.id,c.boundary_id,c.project_id,c.command_id,c.actor_id,c.attempt_id,c.run_id,c.command,c.command_digest,c.receipt,c.receipt_digest FROM budget_jobs j LEFT JOIN budget_operator_commands c ON c.id=j.cancel_command_id LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}
