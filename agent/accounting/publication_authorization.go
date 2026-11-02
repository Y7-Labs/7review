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

// AuthorizePublication records human disclosure authority only. It does not
// enqueue, send, merge, activate memory, or mutate the terminal assessment.
func (a *Authority) AuthorizePublication(ctx context.Context, actor review.OperatorActor, c review.AuthorizePublicationCommand) (*review.PublicationAuthorizationReceipt, error) {
	if err := a.cfg.Operators.ValidateActor(actor); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(struct {
		BoundaryID, ProjectID, ActorID string
		Command                        review.AuthorizePublicationCommand
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
	record, err := a.operatorAttemptForRolesTx(ctx, tx, actor, c.AttemptID, true, review.OperatorRoleReviewer, review.OperatorRolePublisher)
	if err != nil {
		return nil, err
	}
	v := record.view
	conflict := func(reason, current string) error {
		return &OperatorCommandConflict{CurrentAttemptID: current, ControlVersion: v.ControlVersion, Reason: reason}
	}
	var priorCommand, priorReceipt []byte
	var priorDigest, receiptDigest, priorActor, priorAttempt, priorRun, priorID string
	err = tx.QueryRowContext(ctx, `SELECT command,command_digest,receipt,receipt_digest,actor_id,attempt_id,run_id,command_id FROM budget_operator_commands WHERE id=$1 AND boundary_id=$2 AND project_id=$3`, key, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&priorCommand, &priorDigest, &priorReceipt, &receiptDigest, &priorActor, &priorAttempt, &priorRun, &priorID)
	if err == nil {
		if len(priorCommand) > 16<<10 || len(priorReceipt) > 16<<10 || hash(priorCommand) != priorDigest || hash(priorReceipt) != receiptDigest {
			return nil, ErrIntegrity
		}
		if priorDigest != digest || !bytes.Equal(priorCommand, payload) {
			return nil, conflict("command ID already used with different actor or payload", c.AttemptID)
		}
		var receipt review.PublicationAuthorizationReceipt
		if json.Unmarshal(priorReceipt, &receipt) != nil || receipt.SchemaVersion != 1 || receipt.CreatedAt.IsZero() || priorActor != receipt.ActorID || priorAttempt != c.AttemptID || priorRun != v.RunID || priorID != c.CommandID || receipt.CommandID != c.CommandID || receipt.CommandDigest != digest || receipt.AttemptID != c.AttemptID || receipt.ActorID != actor.PrincipalID() || receipt.Kind != c.Kind || receipt.Revision != c.Revision || receipt.ControlVersion != c.ExpectedVersion+1 || receipt.ControlVersion > v.ControlVersion || receipt.AssessmentDigest != c.Payload.AssessmentDigest || receipt.ArtifactDigest != c.Payload.ArtifactDigest || receipt.Destination != c.Payload.Destination || receipt.State != "authorized" {
			return nil, ErrIntegrity
		}
		var pointer sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT publication_authorization_id FROM budget_jobs WHERE run_id=$1`, v.RunID).Scan(&pointer); err != nil || !pointer.Valid {
			return nil, ErrIntegrity
		}
		if err = a.verifyPublicationTargetTx(ctx, tx, record, c); err != nil {
			return nil, err
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
		var currentVersion uint64
		if err = tx.QueryRowContext(ctx, `SELECT j.control_version FROM budget_jobs j JOIN budget_inbox i ON i.run_id=j.run_id WHERE j.attempt_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND convert_from(j.receipt,'UTF8')::jsonb->>'Duplicate'='false'`, current.String, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&currentVersion); err != nil {
			return nil, ErrIntegrity
		}
		v.ControlVersion = currentVersion
		return nil, conflict("attempt is no longer current", current.String)
	}
	if c.ExpectedVersion != v.ControlVersion || c.Revision != v.Revision {
		return nil, conflict("stale control version or revision", c.AttemptID)
	}
	if !v.Active || !v.Current || v.InboxState != "completed" || v.JobPhase != "finished" || record.cancelID.Valid {
		return nil, conflict("attempt has no authorizable terminal assessment", c.AttemptID)
	}
	if err = a.verifyPublicationTargetTx(ctx, tx, record, c); err != nil {
		return nil, err
	}
	receipt := review.PublicationAuthorizationReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: digest, AttemptID: c.AttemptID, ActorID: actor.PrincipalID(), Kind: c.Kind, Revision: c.Revision, ControlVersion: v.ControlVersion + 1, AssessmentDigest: c.Payload.AssessmentDigest, ArtifactDigest: c.Payload.ArtifactDigest, Destination: c.Payload.Destination, State: "authorized"}
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&receipt.CreatedAt); err != nil {
		return nil, err
	}
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	raw, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_operator_commands(id,boundary_id,project_id,command_id,actor_id,attempt_id,run_id,command,command_digest,receipt,receipt_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, key, a.cfg.BoundaryID, a.cfg.ProjectID, c.CommandID, actor.PrincipalID(), c.AttemptID, v.RunID, payload, digest, raw, hash(raw)); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE budget_jobs SET publication_authorization_id=$2,control_version=control_version+1,updated_at=clock_timestamp() WHERE run_id=$1 AND control_version=$3`, v.RunID, key, v.ControlVersion)
	if err != nil {
		return nil, err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return nil, ErrIntegrity
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func (a *Authority) verifyPublicationTargetTx(ctx context.Context, tx *sql.Tx, record operatorAttemptRecord, c review.AuthorizePublicationCommand) error {
	change := record.binding.Attempt.Change
	want := review.PublicationDestination{Provider: change.Provider, Host: change.Host, RepositoryID: change.RepositoryID, ChangeNumber: change.ChangeNumber, ArtifactClass: "assessment"}
	if c.Payload.Destination != want {
		return errors.New("publication destination does not match the exact attempt")
	}
	var reductionID, actionID, assessmentDigest string
	err := tx.QueryRowContext(ctx, `SELECT j.terminal_reduction_id,min(x.action_id),m.artifact_digest FROM budget_jobs j JOIN budget_model_reductions m ON m.id=j.terminal_reduction_id JOIN budget_model_reduction_inputs x ON x.reduction_id=m.id WHERE j.run_id=$1 AND j.attempt_id=$2 GROUP BY j.terminal_reduction_id,m.artifact_digest`, record.view.RunID, c.AttemptID).Scan(&reductionID, &actionID, &assessmentDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("validated terminal assessment unavailable")
	}
	if err != nil {
		return err
	}
	artifact, err := a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return err
	}
	if artifact.State != review.ModelReductionValidated || reductionKey(record.view.RunID, artifact.GroupDigest) != reductionID || artifact.Source.Attempt.AttemptID != c.AttemptID || artifact.Source.Run.ID != record.view.RunID || artifact.Source.Report.Draft == "" || artifact.Source.Report.Final != "" || assessmentDigest != c.Payload.AssessmentDigest || hash([]byte(artifact.Source.Report.Draft)) != c.Payload.ArtifactDigest || !reflect.DeepEqual(artifact.Source.Attempt, record.binding.Attempt) {
		return errors.New("publication target does not match the validated terminal assessment")
	}
	return a.checkTerminalLineageTx(ctx, tx, reductionID, artifact)
}
