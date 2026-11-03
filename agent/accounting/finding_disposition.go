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

type storedFindingDispositionEnvelope struct {
	BoundaryID string
	ProjectID  string
	ActorID    string
	Command    review.FindingDispositionCommand
}

var ErrOperatorFindingNotFound = errors.New("operator finding not found")
var ErrOperatorAttemptContext = errors.New("operator attempt does not match SCM comment context")

func (a *Authority) PriorFindingDispositionCommand(ctx context.Context, actor review.OperatorActor, commandID, attemptID, findingID string, decision review.FindingDecision, reason string) (*review.FindingDispositionCommand, error) {
	if err := a.cfg.Operators.ValidateActor(actor); err != nil {
		return nil, err
	}
	if !validID(commandID) || !validID(attemptID) || !validID(findingID) {
		return nil, ErrOperatorAttemptInvalid
	}
	keyRaw, _ := json.Marshal([]string{a.cfg.BoundaryID, a.cfg.ProjectID, commandID})
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw []byte
	var digest string
	err = tx.QueryRowContext(ctx, `SELECT command,command_digest FROM budget_operator_commands WHERE id=$1 AND boundary_id=$2 AND project_id=$3`, hash(keyRaw), a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&raw, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 16<<10 || hash(raw) != digest {
		return nil, ErrIntegrity
	}
	var metadata struct {
		BoundaryID string
		ProjectID  string
		ActorID    string
		Command    struct{ Kind string }
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.BoundaryID != a.cfg.BoundaryID || metadata.ProjectID != a.cfg.ProjectID {
		return nil, ErrIntegrity
	}
	if metadata.Command.Kind != "disposition_finding" {
		return nil, ErrOperatorCommandConflict
	}
	var envelope storedFindingDispositionEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Command.Validate() != nil || envelope.ActorID != metadata.ActorID {
		return nil, ErrIntegrity
	}
	c := envelope.Command
	if envelope.ActorID != actor.PrincipalID() || c.CommandID != commandID || c.AttemptID != attemptID || c.Payload.FindingID != findingID || c.Payload.Decision != decision || c.Payload.Reason != reason {
		return nil, ErrOperatorCommandConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (a *Authority) DispositionFinding(ctx context.Context, actor review.OperatorActor, c review.FindingDispositionCommand) (*review.FindingDispositionReceipt, error) {
	if err := a.cfg.Operators.ValidateActor(actor); err != nil {
		return nil, err
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(storedFindingDispositionEnvelope{a.cfg.BoundaryID, a.cfg.ProjectID, actor.PrincipalID(), c})
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
	roles := []review.OperatorRole{review.OperatorRoleReviewer, review.OperatorRoleOperator}
	if c.Payload.Decision == review.FindingDisputed {
		roles = append(roles, review.OperatorRoleParticipant)
	}
	record, err := a.operatorAttemptForRolesTx(ctx, tx, actor, c.AttemptID, true, roles...)
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
		var receipt review.FindingDispositionReceipt
		if json.Unmarshal(priorReceipt, &receipt) != nil || receipt.SchemaVersion != 1 || receipt.CreatedAt.IsZero() || priorActor != receipt.ActorID || priorAttempt != c.AttemptID || priorRun != v.RunID || priorID != c.CommandID || receipt.CommandID != c.CommandID || receipt.CommandDigest != digest || receipt.AttemptID != c.AttemptID || receipt.ActorID != actor.PrincipalID() || receipt.Kind != c.Kind || receipt.Revision != c.Revision || receipt.ControlVersion != c.ExpectedVersion+1 || receipt.ControlVersion > v.ControlVersion || receipt.FindingID != c.Payload.FindingID || receipt.FindingDigest != c.Payload.FindingDigest || receipt.Decision != c.Payload.Decision || receipt.State != "recorded" {
			return nil, ErrIntegrity
		}
		var storedAttempt, storedFinding, storedDigest, storedDecision string
		if err = tx.QueryRowContext(ctx, `SELECT attempt_id,finding_id,finding_digest,decision FROM budget_finding_dispositions WHERE id=$1 AND boundary_id=$2 AND project_id=$3`, key, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&storedAttempt, &storedFinding, &storedDigest, &storedDecision); err != nil || storedAttempt != c.AttemptID || storedFinding != c.Payload.FindingID || storedDigest != c.Payload.FindingDigest || storedDecision != string(c.Payload.Decision) {
			return nil, ErrIntegrity
		}
		if _, err = a.terminalFindingTx(ctx, tx, record, c.Payload.FindingID, c.Payload.FindingDigest); err != nil {
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
		return nil, conflict("attempt has no terminal finding to disposition", c.AttemptID)
	}
	if _, err = a.terminalFindingTx(ctx, tx, record, c.Payload.FindingID, c.Payload.FindingDigest); err != nil {
		return nil, err
	}
	receipt := review.FindingDispositionReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: digest, AttemptID: c.AttemptID, ActorID: actor.PrincipalID(), Kind: c.Kind, Revision: c.Revision, ControlVersion: v.ControlVersion + 1, FindingID: c.Payload.FindingID, FindingDigest: c.Payload.FindingDigest, Decision: c.Payload.Decision, State: "recorded"}
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&receipt.CreatedAt); err != nil {
		return nil, err
	}
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	raw, _ := json.Marshal(receipt)
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_operator_commands(id,boundary_id,project_id,command_id,actor_id,attempt_id,run_id,command,command_digest,receipt,receipt_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, key, a.cfg.BoundaryID, a.cfg.ProjectID, c.CommandID, actor.PrincipalID(), c.AttemptID, v.RunID, payload, digest, raw, hash(raw)); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_finding_dispositions(id,boundary_id,project_id,attempt_id,finding_id,finding_digest,decision,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, key, a.cfg.BoundaryID, a.cfg.ProjectID, c.AttemptID, c.Payload.FindingID, c.Payload.FindingDigest, c.Payload.Decision, receipt.CreatedAt); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_finding_disposition_heads(boundary_id,project_id,attempt_id,finding_id,disposition_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(boundary_id,project_id,attempt_id,finding_id) DO UPDATE SET disposition_id=excluded.disposition_id,updated_at=clock_timestamp()`, a.cfg.BoundaryID, a.cfg.ProjectID, c.AttemptID, c.Payload.FindingID, key); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE budget_jobs SET control_version=control_version+1,updated_at=clock_timestamp() WHERE run_id=$1 AND control_version=$2`, v.RunID, v.ControlVersion)
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

func (a *Authority) terminalFindingTx(ctx context.Context, tx *sql.Tx, record operatorAttemptRecord, findingID, findingDigest string) (review.Finding, error) {
	finding, digest, err := a.terminalFindingSubjectTx(ctx, tx, record, findingID)
	if err != nil {
		return review.Finding{}, err
	}
	if digest != findingDigest {
		return review.Finding{}, errors.New("finding digest does not match the terminal assessment")
	}
	return finding, nil
}

func (a *Authority) terminalFindingSubjectTx(ctx context.Context, tx *sql.Tx, record operatorAttemptRecord, findingID string) (review.Finding, string, error) {
	var reductionID, actionID string
	err := tx.QueryRowContext(ctx, `SELECT j.terminal_reduction_id,min(x.action_id) FROM budget_jobs j JOIN budget_model_reductions m ON m.id=j.terminal_reduction_id JOIN budget_model_reduction_inputs x ON x.reduction_id=m.id WHERE j.run_id=$1 AND j.attempt_id=$2 GROUP BY j.terminal_reduction_id`, record.view.RunID, record.view.AttemptID).Scan(&reductionID, &actionID)
	if errors.Is(err, sql.ErrNoRows) {
		return review.Finding{}, "", errors.New("validated terminal assessment unavailable")
	}
	if err != nil {
		return review.Finding{}, "", err
	}
	artifact, err := a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return review.Finding{}, "", err
	}
	if artifact.State != review.ModelReductionValidated || reductionKey(record.view.RunID, artifact.GroupDigest) != reductionID || artifact.Source.Attempt.AttemptID != record.view.AttemptID || artifact.Source.Run.ID != record.view.RunID || !reflect.DeepEqual(artifact.Source.Attempt, record.binding.Attempt) {
		return review.Finding{}, "", ErrIntegrity
	}
	if err = a.checkTerminalLineageTx(ctx, tx, reductionID, artifact); err != nil {
		return review.Finding{}, "", err
	}
	var found *review.Finding
	for index := range artifact.Source.Findings {
		if artifact.Source.Findings[index].ID != findingID {
			continue
		}
		if found != nil {
			return review.Finding{}, "", ErrIntegrity
		}
		candidate := artifact.Source.Findings[index]
		found = &candidate
	}
	if found == nil {
		return review.Finding{}, "", ErrOperatorFindingNotFound
	}
	digest, err := review.CanonicalFindingDigest(*found)
	if err != nil {
		return review.Finding{}, "", errors.New("finding digest does not match the terminal assessment")
	}
	return *found, digest, nil
}

func (a *Authority) FindingDisposition(ctx context.Context, actor review.OperatorActor, attemptID, findingID string) (*review.FindingDispositionView, error) {
	return a.findingDisposition(ctx, actor, attemptID, findingID, nil)
}

func (a *Authority) FindingDispositionInChange(ctx context.Context, actor review.OperatorActor, attemptID, findingID string, change review.ChangeKey) (*review.FindingDispositionView, error) {
	if err := change.Validate(); err != nil || change.Provider == "local" {
		return nil, ErrOperatorAttemptContext
	}
	return a.findingDisposition(ctx, actor, attemptID, findingID, &change)
}

func (a *Authority) findingDisposition(ctx context.Context, actor review.OperatorActor, attemptID, findingID string, expectedChange *review.ChangeKey) (*review.FindingDispositionView, error) {
	if err := a.cfg.Operators.ValidateActor(actor); err != nil {
		return nil, err
	}
	if !validID(attemptID) || !validID(findingID) {
		return nil, ErrOperatorAttemptInvalid
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := a.operatorAttemptForRolesTx(ctx, tx, actor, attemptID, false, review.OperatorRoleReviewer, review.OperatorRoleOperator, review.OperatorRoleParticipant)
	if err != nil {
		return nil, err
	}
	if expectedChange != nil && !reflect.DeepEqual(record.binding.Attempt.Change, *expectedChange) {
		return nil, ErrOperatorAttemptContext
	}
	finding, digest, err := a.terminalFindingSubjectTx(ctx, tx, record, findingID)
	if err != nil {
		return nil, err
	}
	view := &review.FindingDispositionView{SchemaVersion: 1, AttemptID: attemptID, Revision: record.view.Revision, ControlVersion: record.view.ControlVersion, Finding: finding, FindingDigest: digest}
	var commandRaw, receiptRaw []byte
	var commandDigest, receiptDigest, actorID, storedDigest, decision string
	err = tx.QueryRowContext(ctx, `SELECT c.command,c.command_digest,c.receipt,c.receipt_digest,c.actor_id,d.finding_digest,d.decision FROM budget_finding_disposition_heads h JOIN budget_finding_dispositions d ON d.id=h.disposition_id JOIN budget_operator_commands c ON c.id=d.id WHERE h.boundary_id=$1 AND h.project_id=$2 AND h.attempt_id=$3 AND h.finding_id=$4`, a.cfg.BoundaryID, a.cfg.ProjectID, attemptID, findingID).Scan(&commandRaw, &commandDigest, &receiptRaw, &receiptDigest, &actorID, &storedDigest, &decision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if len(commandRaw) > 16<<10 || len(receiptRaw) > 16<<10 || hash(commandRaw) != commandDigest || hash(receiptRaw) != receiptDigest || storedDigest != digest {
			return nil, ErrIntegrity
		}
		var envelope storedFindingDispositionEnvelope
		if json.Unmarshal(commandRaw, &envelope) != nil || envelope.BoundaryID != a.cfg.BoundaryID || envelope.ProjectID != a.cfg.ProjectID || envelope.ActorID != actorID || envelope.Command.AttemptID != attemptID || envelope.Command.Payload.FindingID != findingID || envelope.Command.Payload.FindingDigest != digest || string(envelope.Command.Payload.Decision) != decision || envelope.Command.Validate() != nil {
			return nil, ErrIntegrity
		}
		receipt, decodeErr := review.DecodeFindingDispositionReceipt(receiptRaw, envelope.Command)
		if decodeErr != nil || receipt.ActorID != actorID || receipt.CommandDigest != commandDigest || receipt.ControlVersion > view.ControlVersion {
			return nil, ErrIntegrity
		}
		view.Current = &review.CurrentFindingDisposition{CommandID: envelope.Command.CommandID, ActorID: actorID, Decision: envelope.Command.Payload.Decision, Reason: envelope.Command.Payload.Reason, RecordedVersion: receipt.ControlVersion, CreatedAt: receipt.CreatedAt}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return view, nil
}

func checkFindingDispositionStorage(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT d.id,d.boundary_id,d.project_id,d.attempt_id,d.finding_id,d.finding_digest,d.decision,d.created_at,h.boundary_id,h.project_id,h.attempt_id,h.finding_id,h.disposition_id,h.updated_at FROM budget_finding_dispositions d LEFT JOIN budget_finding_disposition_heads h ON h.disposition_id=d.id LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}
