package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type PublicationOperation struct {
	ID                     string
	AuthorizationCommandID string
	RunID                  string
	AttemptID              string
	AssessmentDigest       string
	ArtifactDigest         string
	Destination            review.PublicationDestination
	Payload                []byte
	PayloadDigest          string
	State                  review.DeliveryState
	WorkerID               string
	Fence                  uint64
	LeaseUntil             time.Time
	SendingAt              time.Time
	DeliveryReceipt        PublicationDeliveryReceipt
	CompletedAt            time.Time
	ReconcileAfter         time.Time
	CreatedAt, UpdatedAt   time.Time
}

type publicationCandidate struct {
	runID, attemptID, authorizationKey          string
	commandRaw, receiptRaw, bindingRaw          []byte
	commandDigest, receiptDigest, bindingDigest string
	active, current                             bool
	tracking, cancelID                          sql.NullString
	inboxState, phase                           string
	controlVersion                              uint64
}

// MaterializeAuthorizedPublications creates an immutable effect record from a
// committed human authorization. It performs no provider I/O.
func (a *Authority) MaterializeAuthorizedPublications(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 128 {
		return 0, errors.New("publication materialization limit must be between 1 and 128")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = a.obsoleteStalePublicationOperationsTx(ctx, tx, limit); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT j.run_id,j.attempt_id,j.publication_authorization_id,c.command,c.command_digest,c.receipt,c.receipt_digest,a.binding,a.binding_digest,a.active,a.tracking_id,coalesce(h.current_attempt_id=a.id,false),j.cancel_command_id,i.state,j.phase,j.control_version
FROM budget_jobs j JOIN budget_inbox i ON i.run_id=j.run_id JOIN budget_attempts a ON a.id=j.attempt_id
JOIN budget_operator_commands c ON c.id=j.publication_authorization_id LEFT JOIN budget_heads h ON h.id=a.tracking_id
WHERE i.boundary_id=$1 AND i.project_id=$2 AND NOT EXISTS (SELECT 1 FROM budget_publication_operations o WHERE o.authorization_id=j.publication_authorization_id)
ORDER BY j.updated_at,j.run_id FOR UPDATE OF j SKIP LOCKED LIMIT $3`, a.cfg.BoundaryID, a.cfg.ProjectID, limit)
	if err != nil {
		return 0, err
	}
	var candidates []publicationCandidate
	for rows.Next() {
		var c publicationCandidate
		if err = rows.Scan(&c.runID, &c.attemptID, &c.authorizationKey, &c.commandRaw, &c.commandDigest, &c.receiptRaw, &c.receiptDigest, &c.bindingRaw, &c.bindingDigest, &c.active, &c.tracking, &c.current, &c.cancelID, &c.inboxState, &c.phase, &c.controlVersion); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	created := 0
	for _, candidate := range candidates {
		if err = a.materializePublicationTx(ctx, tx, candidate); err != nil {
			return 0, err
		}
		created++
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return created, nil
}

func (a *Authority) obsoleteStalePublicationOperationsTx(ctx context.Context, tx *sql.Tx, limit int) error {
	rows, err := tx.QueryContext(ctx, `SELECT o.command_id FROM budget_publication_operations o
JOIN budget_jobs j ON j.run_id=o.run_id JOIN budget_inbox i ON i.run_id=j.run_id
JOIN budget_attempts a ON a.id=o.attempt_id LEFT JOIN budget_heads h ON h.id=a.tracking_id
WHERE o.boundary_id=$1 AND o.project_id=$2 AND o.state='pending'
AND (j.publication_authorization_id IS DISTINCT FROM o.authorization_id OR NOT a.active OR h.current_attempt_id IS DISTINCT FROM a.id OR i.state<>'completed' OR j.phase<>'finished' OR j.cancel_command_id IS NOT NULL)
ORDER BY o.created_at,o.id FOR UPDATE OF o SKIP LOCKED LIMIT $3`, a.cfg.BoundaryID, a.cfg.ProjectID, limit)
	if err != nil {
		return err
	}
	var commandIDs []string
	for rows.Next() {
		var commandID string
		if err = rows.Scan(&commandID); err != nil {
			rows.Close()
			return err
		}
		commandIDs = append(commandIDs, commandID)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, commandID := range commandIDs {
		op, _, readErr := a.readPublicationOperationTx(ctx, tx, commandID)
		if readErr != nil {
			return readErr
		}
		if updateErr := a.obsoletePublicationTx(ctx, tx, op.ID); updateErr != nil {
			return updateErr
		}
	}
	return nil
}

func (a *Authority) materializePublicationTx(ctx context.Context, tx *sql.Tx, candidate publicationCandidate) error {
	receipt, artifact, err := a.authorizedPublicationArtifactTx(ctx, tx, candidate)
	if err != nil {
		return err
	}
	destinationRaw, err := json.Marshal(receipt.Destination)
	if err != nil {
		return err
	}
	payload := []byte(artifact.Source.Report.Draft)
	state := review.DeliveryPending
	if !candidate.active || !candidate.current || candidate.inboxState != "completed" || candidate.phase != "finished" || candidate.cancelID.Valid {
		state = review.DeliveryObsolete
	}
	keyRaw, _ := json.Marshal([]string{a.cfg.BoundaryID, a.cfg.ProjectID, "publication", candidate.authorizationKey})
	_, err = tx.ExecContext(ctx, `INSERT INTO budget_publication_operations(id,boundary_id,project_id,authorization_id,command_id,run_id,attempt_id,assessment_digest,artifact_digest,destination,destination_digest,payload,payload_digest,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, hash(keyRaw), a.cfg.BoundaryID, a.cfg.ProjectID, candidate.authorizationKey, receipt.CommandID, candidate.runID, candidate.attemptID, receipt.AssessmentDigest, receipt.ArtifactDigest, destinationRaw, hash(destinationRaw), payload, hash(payload), state)
	return err
}

func (a *Authority) authorizedPublicationArtifactTx(ctx context.Context, tx *sql.Tx, candidate publicationCandidate) (review.PublicationAuthorizationReceipt, review.ModelReduction, error) {
	if len(candidate.commandRaw) > 16<<10 || len(candidate.receiptRaw) > 16<<10 || len(candidate.bindingRaw) > 1<<20 || hash(candidate.commandRaw) != candidate.commandDigest || hash(candidate.receiptRaw) != candidate.receiptDigest || hash(candidate.bindingRaw) != candidate.bindingDigest {
		return review.PublicationAuthorizationReceipt{}, review.ModelReduction{}, ErrIntegrity
	}
	var envelope struct {
		BoundaryID string                             `json:"BoundaryID"`
		ProjectID  string                             `json:"ProjectID"`
		ActorID    string                             `json:"ActorID"`
		Command    review.AuthorizePublicationCommand `json:"Command"`
	}
	if json.Unmarshal(candidate.commandRaw, &envelope) != nil || envelope.BoundaryID != a.cfg.BoundaryID || envelope.ProjectID != a.cfg.ProjectID || !validID(envelope.ActorID) || envelope.Command.AttemptID != candidate.attemptID {
		return review.PublicationAuthorizationReceipt{}, review.ModelReduction{}, ErrIntegrity
	}
	receipt, err := review.DecodePublicationAuthorizationReceipt(candidate.receiptRaw, envelope.Command)
	if err != nil || receipt.ActorID != envelope.ActorID || receipt.CommandDigest != candidate.commandDigest || receipt.ControlVersion > candidate.controlVersion {
		return review.PublicationAuthorizationReceipt{}, review.ModelReduction{}, ErrIntegrity
	}
	var binding attemptBinding
	if json.Unmarshal(candidate.bindingRaw, &binding) != nil || binding.ProjectID != a.cfg.ProjectID || binding.Attempt.AttemptID != candidate.attemptID {
		return review.PublicationAuthorizationReceipt{}, review.ModelReduction{}, ErrIntegrity
	}
	aggregate, err := a.readAggregateTx(ctx, tx, candidate.runID)
	if err != nil {
		return review.PublicationAuthorizationReceipt{}, review.ModelReduction{}, err
	}
	if !reflect.DeepEqual(aggregate.Receipt.Attempt, binding.Attempt) || !reflect.DeepEqual(aggregate.Receipt.Execution, binding.Execution) {
		return review.PublicationAuthorizationReceipt{}, review.ModelReduction{}, ErrIntegrity
	}
	record := operatorAttemptRecord{view: OperatorAttemptView{AttemptID: candidate.attemptID, RunID: candidate.runID, Active: candidate.active, Current: candidate.current, InboxState: candidate.inboxState, JobPhase: candidate.phase, ControlVersion: candidate.controlVersion, Revision: binding.Attempt.Snapshot.AnalysisRevision()}, binding: binding, bindingDigest: candidate.bindingDigest, tracking: candidate.tracking, cancelID: candidate.cancelID}
	artifact, err := a.publicationTargetTx(ctx, tx, record, envelope.Command)
	if err != nil {
		return review.PublicationAuthorizationReceipt{}, review.ModelReduction{}, err
	}
	return receipt, artifact, nil
}

func (a *Authority) LookupPublicationOperation(ctx context.Context, commandID string) (PublicationOperation, error) {
	var op PublicationOperation
	if !validID(commandID) {
		return op, errors.New("exact publication command ID required")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return op, err
	}
	defer tx.Rollback()
	op, _, err = a.readPublicationOperationTx(ctx, tx, commandID)
	if err != nil {
		return PublicationOperation{}, err
	}
	if err = tx.Commit(); err != nil {
		return PublicationOperation{}, err
	}
	op.Payload = append([]byte(nil), op.Payload...)
	return op, nil
}

func (a *Authority) readPublicationOperationTx(ctx context.Context, tx *sql.Tx, commandID string) (PublicationOperation, string, error) {
	var op PublicationOperation
	var authorizationKey, destinationDigest string
	var destinationRaw, receiptRaw []byte
	var receiptDigest sql.NullString
	var worker sql.NullString
	var leaseUntil, sendingAt, completedAt, reconcileAfter sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT id,authorization_id,command_id,run_id,attempt_id,assessment_digest,artifact_digest,destination,destination_digest,payload,payload_digest,state,worker_id,fence,lease_until,sending_at,delivery_receipt,delivery_receipt_digest,completed_at,reconcile_after,created_at,updated_at FROM budget_publication_operations WHERE boundary_id=$1 AND project_id=$2 AND command_id=$3`, a.cfg.BoundaryID, a.cfg.ProjectID, commandID).Scan(&op.ID, &authorizationKey, &op.AuthorizationCommandID, &op.RunID, &op.AttemptID, &op.AssessmentDigest, &op.ArtifactDigest, &destinationRaw, &destinationDigest, &op.Payload, &op.PayloadDigest, &op.State, &worker, &op.Fence, &leaseUntil, &sendingAt, &receiptRaw, &receiptDigest, &completedAt, &reconcileAfter, &op.CreatedAt, &op.UpdatedAt)
	if err != nil {
		return PublicationOperation{}, "", err
	}
	keyRaw, _ := json.Marshal([]string{a.cfg.BoundaryID, a.cfg.ProjectID, "publication", authorizationKey})
	if op.ID != hash(keyRaw) || hash(destinationRaw) != destinationDigest || hash(op.Payload) != op.PayloadDigest || op.PayloadDigest != op.ArtifactDigest || json.Unmarshal(destinationRaw, &op.Destination) != nil || op.Destination.Validate() != nil {
		return PublicationOperation{}, "", ErrIntegrity
	}
	canonicalDestination, _ := json.Marshal(op.Destination)
	if !bytes.Equal(canonicalDestination, destinationRaw) {
		return PublicationOperation{}, "", ErrIntegrity
	}
	if worker.Valid {
		op.WorkerID = worker.String
	}
	if leaseUntil.Valid {
		op.LeaseUntil = leaseUntil.Time
	}
	if sendingAt.Valid {
		op.SendingAt = sendingAt.Time
	}
	if completedAt.Valid {
		op.CompletedAt = completedAt.Time
	}
	if reconcileAfter.Valid {
		op.ReconcileAfter = reconcileAfter.Time
	}
	if receiptDigest.Valid {
		if hash(receiptRaw) != receiptDigest.String || json.Unmarshal(receiptRaw, &op.DeliveryReceipt) != nil || op.DeliveryReceipt.validate(op.Destination.Provider, op.PayloadDigest) != nil {
			return PublicationOperation{}, "", ErrIntegrity
		}
		canonicalReceipt, _ := json.Marshal(op.DeliveryReceipt)
		if !bytes.Equal(canonicalReceipt, receiptRaw) {
			return PublicationOperation{}, "", ErrIntegrity
		}
	}
	completed := op.State == review.DeliveryDelivered
	uncertain := op.State == review.DeliveryUncertain
	if (op.WorkerID == "") != op.LeaseUntil.IsZero() || (op.State == review.DeliveryPending && !op.SendingAt.IsZero()) || ((op.State == review.DeliverySending || uncertain || completed || op.State == review.DeliveryFailed) && (op.WorkerID == "" || op.Fence == 0 || op.LeaseUntil.IsZero() || op.SendingAt.IsZero())) || completed != receiptDigest.Valid || completed != completedAt.Valid || (!completed && len(receiptRaw) != 0) || uncertain != reconcileAfter.Valid {
		return PublicationOperation{}, "", ErrIntegrity
	}
	return op, authorizationKey, nil
}

func checkPublicationOperationStorage(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.boundary_id,o.project_id,o.authorization_id,o.command_id,o.run_id,o.attempt_id,o.assessment_digest,o.artifact_digest,o.destination,o.destination_digest,o.payload,o.payload_digest,o.state,o.worker_id,o.fence,o.lease_until,o.sending_at,o.delivery_receipt,o.delivery_receipt_digest,o.completed_at,o.reconcile_after,o.created_at,o.updated_at FROM budget_publication_operations o LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

func checkPublicationOperationStorageV22(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.boundary_id,o.project_id,o.authorization_id,o.command_id,o.run_id,o.attempt_id,o.assessment_digest,o.artifact_digest,o.destination,o.destination_digest,o.payload,o.payload_digest,o.state,o.worker_id,o.fence,o.lease_until,o.sending_at,o.delivery_receipt,o.delivery_receipt_digest,o.completed_at,o.created_at,o.updated_at FROM budget_publication_operations o LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

func checkPublicationOperationStorageV21(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.boundary_id,o.project_id,o.authorization_id,o.command_id,o.run_id,o.attempt_id,o.assessment_digest,o.artifact_digest,o.destination,o.destination_digest,o.payload,o.payload_digest,o.state,o.worker_id,o.fence,o.lease_until,o.sending_at,o.created_at,o.updated_at FROM budget_publication_operations o LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

func checkPublicationOperationStorageV20(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT o.id,o.boundary_id,o.project_id,o.authorization_id,o.command_id,o.run_id,o.attempt_id,o.assessment_digest,o.artifact_digest,o.destination,o.destination_digest,o.payload,o.payload_digest,o.state,o.created_at,o.updated_at FROM budget_publication_operations o LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}
