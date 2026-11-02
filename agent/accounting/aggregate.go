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

type acceptanceEvent struct {
	Type       string `json:"type"`
	RunID      string `json:"run_id"`
	AttemptID  string `json:"attempt_id"`
	Generation uint64 `json:"generation"`
}

// AcceptAggregate commits every member of accepted work before acknowledgement.
// It does not issue a dispatch permit.
func (a *Authority) AcceptAggregate(ctx context.Context, req review.Request, input review.Admission, source review.Source, continuation []byte) (*InboxItem, error) {
	if prior, err := a.ReplayAggregate(ctx, req); err != nil || prior != nil {
		return prior, err
	}
	input.Execution.DeadlineAt = input.Execution.DeadlineAt.UTC().Truncate(time.Microsecond)
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := a.acceptInboxTx(ctx, tx, req, time.Until(input.Execution.DeadlineAt))
	if errors.Is(err, ErrInboxConflict) {
		if e := tx.Commit(); e != nil {
			return nil, e
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if !item.created {
		if _, err = a.readAggregateTx(ctx, tx, item.RunID); err != nil {
			return nil, err
		}
		item, err = a.lookupInboxTx(ctx, tx, item.RunID)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return item, nil
	}
	if input.Job != nil || input.Execution.Mode != review.ExecutionService || input.Execution.Persistence != review.PersistenceDurableService || input.DeliveryID != req.DeliveryID || input.PayloadDigest != req.PayloadDigest {
		return nil, errors.New("invalid service aggregate authority")
	}
	if err = validatePreparedSource(source, input); err != nil {
		return nil, err
	}
	// This uncommitted internal claim is revoked before becoming visible. Workers
	// must claim the pending aggregate independently after acceptance commits.
	token, err := review.NewOpaqueID("admission-")
	if err != nil {
		return nil, err
	}
	input.Job = &review.JobClaim{RunID: item.RunID, Token: token}
	input.Execution.DeadlineAt = input.Execution.DeadlineAt.UTC()
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET state='running',claim_token=$2,deadline=$3 WHERE run_id=$1`, item.RunID, token, input.Execution.DeadlineAt); err != nil {
		return nil, err
	}
	receipt, err := a.admitTx(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	verified, err := review.NewVerifiedContext(source.Request, receipt.Attempt, receipt.Execution, receipt.Attestation)
	if err != nil {
		return nil, err
	}
	source = source.Clone()
	source.Attempt = verified.Source.Attempt
	source.Execution = verified.Source.Execution
	source.Attestation = verified.Source.Attestation
	source.Investigation = verified.Source.Investigation
	source.Policy = review.Source{Policy: receipt.Policy}.Clone().Policy
	source.Run.ID = item.RunID
	aggregate := review.AcceptedAggregate{Version: 1, OriginalRequest: req, Receipt: receipt, Source: source, Continuation: append([]byte(nil), continuation...)}
	raw, err := json.Marshal(aggregate)
	if err != nil {
		return nil, err
	}
	if len(raw) > 16<<20 {
		return nil, errors.New("prepared aggregate exceeds storage bound")
	}
	event, _ := json.Marshal(acceptanceEvent{Type: "aggregate_accepted", RunID: item.RunID, AttemptID: receipt.Attempt.AttemptID, Generation: receipt.Attempt.Generation})
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_aggregates(run_id,checkpoint,checkpoint_digest,initial_event) VALUES($1,$2,$3,$4)`, item.RunID, raw, hash(raw), event); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET state='pending',claim_token=NULL WHERE run_id=$1`, item.RunID); err != nil {
		return nil, err
	}
	item, err = a.lookupInboxTx(ctx, tx, item.RunID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func validatePreparedSource(source review.Source, input review.Admission) error {
	if !input.Policy.TriggerAccepted {
		return errors.New("service aggregate trigger was not accepted")
	}
	if source.SCM == nil || source.Attestation == nil || !reflect.DeepEqual(source.Request, input.Request) || !reflect.DeepEqual(source.Policy, input.Policy) || *source.Attestation != input.Attestation {
		return errors.New("prepared source does not match admission")
	}
	_, att, err := review.AttestSCMRequest(source.Request, source.SCM, input.Attestation.VerifiedAt)
	if err != nil {
		return err
	}
	if att.Snapshot != input.Attestation.Snapshot || att.ComparisonTree != input.Attestation.ComparisonTree {
		return errors.New("prepared source snapshot mismatch")
	}
	return nil
}

func (a *Authority) readAggregateTx(ctx context.Context, tx *sql.Tx, runID string) (review.AcceptedAggregate, error) {
	var aggregate review.AcceptedAggregate
	var raw, receiptRaw, eventRaw []byte
	var digest, attemptID string
	err := tx.QueryRowContext(ctx, `SELECT g.checkpoint,g.checkpoint_digest,j.receipt,j.attempt_id,g.initial_event FROM budget_aggregates g JOIN budget_jobs j ON j.run_id=g.run_id JOIN budget_inbox i ON i.run_id=j.run_id WHERE g.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3`, runID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&raw, &digest, &receiptRaw, &attemptID, &eventRaw)
	if err != nil {
		return aggregate, errors.New("complete accepted aggregate unavailable; recovery required")
	}
	if len(raw) > 16<<20 || hash(raw) != digest {
		return aggregate, errors.New("accepted aggregate integrity failure")
	}
	if err = json.Unmarshal(raw, &aggregate); err != nil {
		return aggregate, err
	}
	var event acceptanceEvent
	if err = json.Unmarshal(eventRaw, &event); err != nil {
		return aggregate, err
	}
	if event.Type != "aggregate_accepted" || event.RunID != runID || event.AttemptID != attemptID || event.Generation != aggregate.Receipt.Attempt.Generation {
		return aggregate, errors.New("accepted aggregate event mismatch")
	}
	canonicalReceipt, err := json.Marshal(aggregate.Receipt)
	if err != nil {
		return aggregate, err
	}
	if aggregate.Version != 1 || aggregate.Source.Run.ID != runID || aggregate.Receipt.Attempt.AttemptID != attemptID || !bytes.Equal(canonicalReceipt, receiptRaw) || !reflect.DeepEqual(aggregate.Source.Attempt, aggregate.Receipt.Attempt) || !reflect.DeepEqual(aggregate.Source.Execution, aggregate.Receipt.Execution) || !reflect.DeepEqual(aggregate.Source.Policy, aggregate.Receipt.Policy) {
		return aggregate, errors.New("accepted aggregate identity mismatch")
	}
	if _, err = review.NewVerifiedContext(aggregate.Source.Request, aggregate.Receipt.Attempt, aggregate.Receipt.Execution, aggregate.Receipt.Attestation); err != nil {
		return aggregate, err
	}
	if err = validatePreparedSource(aggregate.Source, review.Admission{Request: aggregate.Source.Request, Policy: aggregate.Receipt.Policy, Attestation: aggregate.Receipt.Attestation}); err != nil {
		return aggregate, err
	}
	return aggregate, nil
}

func (a *Authority) LoadAggregate(ctx context.Context, claim review.JobClaim) (review.AcceptedAggregate, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return review.AcceptedAggregate{}, err
	}
	defer tx.Rollback()
	if _, _, err = a.lockJob(ctx, tx, &claim); err != nil {
		return review.AcceptedAggregate{}, err
	}
	aggregate, err := a.readAggregateTx(ctx, tx, claim.RunID)
	if err != nil {
		return review.AcceptedAggregate{}, err
	}
	if err = tx.Commit(); err != nil {
		return review.AcceptedAggregate{}, err
	}
	return aggregate, nil
}

// ReplayAggregate resolves authenticated transport retries before new SCM I/O.
func (a *Authority) ReplayAggregate(ctx context.Context, req review.Request) (*InboxItem, error) {
	if req.DeliveryID == "" {
		return nil, nil
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	keyRaw, _ := json.Marshal([]string{a.cfg.BoundaryID, a.cfg.ProjectID, req.Provider, req.DeliveryID})
	var runID, fingerprint string
	err = tx.QueryRowContext(ctx, `SELECT run_id,fingerprint FROM budget_inbox WHERE id=$1 FOR UPDATE`, hash(keyRaw)).Scan(&runID, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if hash(raw) != fingerprint {
		id, e := review.NewOpaqueID("conflict-")
		if e != nil {
			return nil, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_inbox_conflicts(id,inbox_id,fingerprint) VALUES($1,$2,$3)`, id, hash(keyRaw), hash(raw)); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrInboxConflict
	}
	if _, err = a.readAggregateTx(ctx, tx, runID); err != nil {
		return nil, err
	}
	item, err := a.lookupInboxTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}
