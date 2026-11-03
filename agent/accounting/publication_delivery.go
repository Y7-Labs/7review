package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

var ErrPublicationObsolete = errors.New("publication operation is no longer current")

type PublicationDeliveryReceipt struct {
	Provider      string `json:"provider"`
	RemoteID      string `json:"remote_id"`
	RemoteURL     string `json:"remote_url,omitempty"`
	PayloadDigest string `json:"payload_digest"`
}

func (r PublicationDeliveryReceipt) validate(provider, payloadDigest string) error {
	if r.Provider != provider || strings.TrimSpace(r.RemoteID) == "" || len(r.RemoteID) > 512 || len(r.RemoteURL) > 4096 || r.PayloadDigest != payloadDigest {
		return errors.New("exact provider delivery receipt required")
	}
	return nil
}

type PublicationClaim struct {
	Operation  PublicationOperation
	WorkerID   string
	Fence      uint64
	LeaseUntil time.Time
}

type PublicationPermit struct {
	OperationID   string
	CommandID     string
	WorkerID      string
	Fence         uint64
	PayloadDigest string
	SendingAt     time.Time
}

func validPublicationLease(worker string, lease time.Duration) error {
	if !validID(worker) || lease < time.Millisecond || lease > 10*time.Minute {
		return errors.New("publication worker and bounded lease are required")
	}
	return nil
}

func (a *Authority) ClaimPublication(ctx context.Context, worker string, lease time.Duration) (*PublicationClaim, error) {
	if err := validPublicationLease(worker, lease); err != nil {
		return nil, err
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = a.obsoleteStalePublicationOperationsTx(ctx, tx, 8); err != nil {
		return nil, err
	}
	var commandID string
	err = tx.QueryRowContext(ctx, `SELECT command_id FROM budget_publication_operations WHERE boundary_id=$1 AND project_id=$2 AND state='pending' AND (worker_id IS NULL OR lease_until<=clock_timestamp()) ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&commandID)
	if errors.Is(err, sql.ErrNoRows) {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	op, authorizationKey, err := a.readPublicationOperationTx(ctx, tx, commandID)
	if err != nil {
		return nil, err
	}
	current, err := a.verifyPublicationOperationAuthorityTx(ctx, tx, op, authorizationKey)
	if err != nil {
		return nil, err
	}
	if !current {
		if err = a.obsoletePublicationTx(ctx, tx, op.ID); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	var fence uint64
	var leaseUntil time.Time
	err = tx.QueryRowContext(ctx, `UPDATE budget_publication_operations SET worker_id=$2,fence=fence+1,lease_until=clock_timestamp()+($3::bigint*interval '1 millisecond'),updated_at=clock_timestamp() WHERE id=$1 AND state='pending' RETURNING fence,lease_until`, op.ID, worker, lease.Milliseconds()).Scan(&fence, &leaseUntil)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	op.WorkerID, op.Fence, op.LeaseUntil = worker, fence, leaseUntil
	op.Payload = append([]byte(nil), op.Payload...)
	return &PublicationClaim{Operation: op, WorkerID: worker, Fence: fence, LeaseUntil: leaseUntil}, nil
}

func (a *Authority) BeginPublication(ctx context.Context, claim PublicationClaim, sendLease time.Duration) (*PublicationPermit, error) {
	if err := validPublicationLease(claim.WorkerID, sendLease); err != nil || claim.Fence == 0 || claim.Operation.ID == "" || claim.Operation.AuthorizationCommandID == "" {
		return nil, errors.New("valid fenced publication claim required")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var commandID string
	var leaseLive bool
	if err = tx.QueryRowContext(ctx, `SELECT command_id,lease_until>clock_timestamp() FROM budget_publication_operations WHERE id=$1 AND boundary_id=$2 AND project_id=$3 FOR UPDATE`, claim.Operation.ID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&commandID, &leaseLive); err != nil {
		return nil, err
	}
	op, authorizationKey, err := a.readPublicationOperationTx(ctx, tx, commandID)
	if err != nil {
		return nil, err
	}
	if op.State != review.DeliveryPending || op.WorkerID != claim.WorkerID || op.Fence != claim.Fence || !leaseLive || op.ID != claim.Operation.ID || op.PayloadDigest != claim.Operation.PayloadDigest {
		return nil, errors.New("publication claim is stale")
	}
	current, err := a.verifyPublicationOperationAuthorityTx(ctx, tx, op, authorizationKey)
	if err != nil {
		return nil, err
	}
	if !current {
		if err = a.obsoletePublicationTx(ctx, tx, op.ID); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrPublicationObsolete
	}
	permit := &PublicationPermit{OperationID: op.ID, CommandID: op.AuthorizationCommandID, WorkerID: claim.WorkerID, Fence: claim.Fence, PayloadDigest: op.PayloadDigest}
	err = tx.QueryRowContext(ctx, `UPDATE budget_publication_operations SET state='sending',sending_at=clock_timestamp(),lease_until=clock_timestamp()+($4::bigint*interval '1 millisecond'),updated_at=clock_timestamp() WHERE id=$1 AND state='pending' AND worker_id=$2 AND fence=$3 RETURNING sending_at`, op.ID, claim.WorkerID, claim.Fence, sendLease.Milliseconds()).Scan(&permit.SendingAt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return permit, nil
}

func (a *Authority) RecoverUncertainPublications(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 128 {
		return 0, errors.New("publication recovery limit must be between 1 and 128")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT command_id FROM budget_publication_operations WHERE boundary_id=$1 AND project_id=$2 AND state='sending' AND lease_until<=clock_timestamp() ORDER BY sending_at,id FOR UPDATE SKIP LOCKED LIMIT $3`, a.cfg.BoundaryID, a.cfg.ProjectID, limit)
	if err != nil {
		return 0, err
	}
	var commandIDs []string
	for rows.Next() {
		var commandID string
		if err = rows.Scan(&commandID); err != nil {
			rows.Close()
			return 0, err
		}
		commandIDs = append(commandIDs, commandID)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	for _, commandID := range commandIDs {
		op, _, readErr := a.readPublicationOperationTx(ctx, tx, commandID)
		if readErr != nil {
			return 0, readErr
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE budget_publication_operations SET state='uncertain',updated_at=clock_timestamp() WHERE id=$1 AND state='sending'`, op.ID)
		if updateErr != nil {
			return 0, updateErr
		}
		if n, rowsErr := result.RowsAffected(); rowsErr != nil || n != 1 {
			return 0, ErrIntegrity
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(commandIDs), nil
}

func (a *Authority) CompletePublication(ctx context.Context, permit PublicationPermit, receipt PublicationDeliveryReceipt) (*PublicationOperation, error) {
	if !validID(permit.OperationID) || !validID(permit.CommandID) || !validID(permit.WorkerID) || permit.Fence == 0 || permit.PayloadDigest == "" {
		return nil, errors.New("valid fenced publication permit required")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var commandID string
	if err = tx.QueryRowContext(ctx, `SELECT command_id FROM budget_publication_operations WHERE id=$1 AND boundary_id=$2 AND project_id=$3 FOR UPDATE`, permit.OperationID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&commandID); err != nil {
		return nil, err
	}
	op, _, err := a.readPublicationOperationTx(ctx, tx, commandID)
	if err != nil {
		return nil, err
	}
	if op.ID != permit.OperationID || op.AuthorizationCommandID != permit.CommandID || op.WorkerID != permit.WorkerID || op.Fence != permit.Fence || op.PayloadDigest != permit.PayloadDigest || receipt.validate(op.Destination.Provider, op.PayloadDigest) != nil {
		return nil, errors.New("publication permit or receipt is stale")
	}
	if op.State == review.DeliveryDelivered {
		if op.DeliveryReceipt != receipt {
			return nil, errors.New("publication completion conflicts with durable receipt")
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		op.Payload = append([]byte(nil), op.Payload...)
		return &op, nil
	}
	if op.State != review.DeliverySending {
		return nil, errors.New("publication is not sending")
	}
	receiptRaw, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	var completedAt time.Time
	if err = tx.QueryRowContext(ctx, `UPDATE budget_publication_operations SET state='delivered',delivery_receipt=$4,delivery_receipt_digest=$5,completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND state='sending' AND worker_id=$2 AND fence=$3 RETURNING completed_at`, op.ID, permit.WorkerID, permit.Fence, receiptRaw, hash(receiptRaw)).Scan(&completedAt); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	op.State = review.DeliveryDelivered
	op.DeliveryReceipt = receipt
	op.CompletedAt = completedAt
	op.Payload = append([]byte(nil), op.Payload...)
	return &op, nil
}

func (a *Authority) obsoletePublicationTx(ctx context.Context, tx *sql.Tx, operationID string) error {
	result, err := tx.ExecContext(ctx, `UPDATE budget_publication_operations SET state='obsolete',worker_id=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE id=$1 AND state='pending'`, operationID)
	if err != nil {
		return err
	}
	if n, rowsErr := result.RowsAffected(); rowsErr != nil || n != 1 {
		return ErrIntegrity
	}
	return nil
}

func (a *Authority) verifyPublicationOperationAuthorityTx(ctx context.Context, tx *sql.Tx, op PublicationOperation, authorizationKey string) (bool, error) {
	var candidate publicationCandidate
	err := tx.QueryRowContext(ctx, `SELECT j.run_id,j.attempt_id,c.id,c.command,c.command_digest,c.receipt,c.receipt_digest,a.binding,a.binding_digest,a.active,a.tracking_id,coalesce(h.current_attempt_id=a.id,false),j.cancel_command_id,i.state,j.phase,j.control_version
FROM budget_jobs j JOIN budget_inbox i ON i.run_id=j.run_id JOIN budget_attempts a ON a.id=j.attempt_id
JOIN budget_operator_commands c ON c.id=$1 LEFT JOIN budget_heads h ON h.id=a.tracking_id
WHERE j.run_id=$2 AND j.attempt_id=$3 AND i.boundary_id=$4 AND i.project_id=$5 FOR SHARE OF j`, authorizationKey, op.RunID, op.AttemptID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&candidate.runID, &candidate.attemptID, &candidate.authorizationKey, &candidate.commandRaw, &candidate.commandDigest, &candidate.receiptRaw, &candidate.receiptDigest, &candidate.bindingRaw, &candidate.bindingDigest, &candidate.active, &candidate.tracking, &candidate.current, &candidate.cancelID, &candidate.inboxState, &candidate.phase, &candidate.controlVersion)
	if err != nil {
		return false, err
	}
	receipt, artifact, err := a.authorizedPublicationArtifactTx(ctx, tx, candidate)
	if err != nil {
		return false, err
	}
	if receipt.CommandID != op.AuthorizationCommandID || receipt.AssessmentDigest != op.AssessmentDigest || receipt.ArtifactDigest != op.ArtifactDigest || receipt.Destination != op.Destination || hash([]byte(artifact.Source.Report.Draft)) != op.PayloadDigest || !bytes.Equal([]byte(artifact.Source.Report.Draft), op.Payload) {
		return false, ErrIntegrity
	}
	current := candidate.active && candidate.current && candidate.inboxState == "completed" && candidate.phase == "finished" && !candidate.cancelID.Valid
	if current {
		var pointer sql.NullString
		if err = tx.QueryRowContext(ctx, `SELECT publication_authorization_id FROM budget_jobs WHERE run_id=$1`, op.RunID).Scan(&pointer); err != nil {
			return false, err
		}
		current = pointer.Valid && pointer.String == authorizationKey
	}
	return current, nil
}
