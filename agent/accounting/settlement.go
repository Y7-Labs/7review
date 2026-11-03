package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

var ErrIntegrity = errors.New("budget integrity incident blocks further spending")

type FinalReceipt struct {
	SchemaVersion                                                                         int
	ReceiptID, ActionID, RequestDigest, Provider, Model, Currency, EvidenceRef, Certainty string
	Actual                                                                                review.ResourceLimitScope
}

// ReceiptVerifier is trusted reconciliation, not a worker's UsageKnown flag.
// Implementations must authenticate provider usage or an authorized operator's
// final settlement and bind every receipt field. No default verifier is supplied.
type ReceiptVerifier interface {
	VerifyFinal(context.Context, FinalReceipt) error
}

func (a *Authority) SettleFinal(ctx context.Context, r FinalReceipt) error {
	if a.cfg.ReceiptVerifier == nil {
		return errors.New("no qualified final receipt verifier")
	}
	if err := validateFinalReceipt(r); err != nil {
		return err
	}
	if err := a.cfg.ReceiptVerifier.VerifyFinal(ctx, r); err != nil {
		return err
	}
	return a.settleFinal(ctx, r, nil)
}

func (a *Authority) SettleFinalByOperator(ctx context.Context, actor review.OperatorActor, r FinalReceipt, evidenceID string) error {
	if a.cfg.Operators == nil {
		return review.ErrOperatorUnauthorized
	}
	if err := a.cfg.Operators.ValidateActor(actor); err != nil {
		return err
	}
	if !validID(evidenceID) {
		return errors.New("exact operator settlement evidence ID required")
	}
	r.EvidenceRef = "operator:" + actor.PrincipalID() + ":" + evidenceID
	if err := validateFinalReceipt(r); err != nil {
		return err
	}
	return a.settleFinal(ctx, r, &actor)
}

func validateFinalReceipt(r FinalReceipt) error {
	if r.SchemaVersion != 1 || r.Certainty != "final" || !validID(r.ReceiptID) || !validID(r.ActionID) || !canonicalDigest(r.RequestDigest) || !validID(r.Provider) || !validID(r.Model) || r.Currency == "" || r.EvidenceRef == "" || len(r.EvidenceRef) > 4096 || r.Actual.Period != nil || r.Actual.ModelCalls != 1 || r.Actual.ToolCalls != 0 || r.Actual.ObservationBytes != 0 {
		return errors.New("invalid final model usage receipt")
	}
	for _, value := range amounts(r.Actual) {
		if value < 0 {
			return errors.New("negative actual usage")
		}
	}
	return nil
}

func (a *Authority) settleFinal(ctx context.Context, r FinalReceipt, actor *review.OperatorActor) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Receipt identity precedes scope locks even for unrelated projects.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, r.ReceiptID); err != nil {
		return err
	}
	if err = lockActionScopes(ctx, tx, r.ActionID); err != nil {
		return err
	}
	var request, quoteJSON, upperJSON []byte
	var settled, incident bool
	var digest, state string
	err = tx.QueryRowContext(ctx, `SELECT request_digest,request,quote,upper_amount,settled,integrity_incident,state FROM budget_actions WHERE id=$1 FOR UPDATE`, r.ActionID).Scan(&digest, &request, &quoteJSON, &upperJSON, &settled, &incident, &state)
	if errors.Is(err, sql.ErrNoRows) {
		if actor != nil {
			return ErrOperatorAttemptNotFound
		}
		return quarantine(ctx, tx, r, raw, "receipt has no matching action")
	}
	if err != nil {
		return err
	}
	var intent orchestrator.DispatchIntent
	var quote Quote
	var upper review.ResourceLimitScope
	if err = json.Unmarshal(request, &intent); err != nil {
		return err
	}
	if err = json.Unmarshal(quoteJSON, &quote); err != nil {
		return err
	}
	if err = json.Unmarshal(upperJSON, &upper); err != nil {
		return err
	}
	if actor != nil {
		if err = a.cfg.Operators.Authorize(*actor, a.cfg.BoundaryID, a.cfg.ProjectID, intent.Attempt.Change, review.OperatorRoleOperator); err != nil {
			return err
		}
	}
	if state == "prepared" {
		return quarantine(ctx, tx, r, raw, "receipt precedes sending marker")
	}
	if r.RequestDigest != digest || r.Provider != intent.Provider || r.Model != intent.Model || r.Currency != quote.Currency {
		return quarantine(ctx, tx, r, raw, "receipt action/request/route/currency mismatch")
	}
	if state == "not_executed" {
		if _, err = tx.ExecContext(ctx, `UPDATE budget_actions SET integrity_incident=true WHERE id=$1`, r.ActionID); err != nil {
			return err
		}
		if err = recordQuarantine(ctx, tx, r, raw, "qualified usage contradicts a not-executed proof"); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return ErrIntegrity
	}
	var priorDigest string
	err = tx.QueryRowContext(ctx, `SELECT payload_digest FROM budget_receipts WHERE id=$1`, r.ReceiptID).Scan(&priorDigest)
	if err == nil {
		if priorDigest != hash(raw) {
			return quarantine(ctx, tx, r, raw, "receipt identity conflicts with recorded usage")
		}
		if incident {
			return ErrIntegrity
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if settled {
		return quarantine(ctx, tx, r, raw, "action already has a different final receipt")
	}
	actual, _ := json.Marshal(r.Actual)
	incident = incident || exceeds(r.Actual, upper)
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_receipts(id,action_id,payload,payload_digest) VALUES($1,$2,$3,$4)`, r.ReceiptID, r.ActionID, raw, hash(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_actions SET settled=true,actual_amount=$2,receipt_id=$3,integrity_incident=$4 WHERE id=$1`, r.ActionID, actual, r.ReceiptID, incident); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if incident {
		return ErrIntegrity
	}
	return nil
}

func quarantine(ctx context.Context, tx *sql.Tx, r FinalReceipt, raw []byte, reason string) error {
	if err := recordQuarantine(ctx, tx, r, raw, reason); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return errors.New(reason)
}

func recordQuarantine(ctx context.Context, tx *sql.Tx, r FinalReceipt, raw []byte, reason string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO budget_quarantine(id,receipt_id,payload,reason) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING`, hash(append(append([]byte(nil), raw...), []byte(reason)...)), r.ReceiptID, raw, reason)
	return err
}

func lockActionScopes(ctx context.Context, tx *sql.Tx, actionID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.id FROM budget_scopes s JOIN (SELECT scope_id FROM budget_holds WHERE action_id=$1 UNION SELECT scope_id FROM budget_unused_release_scopes WHERE action_id=$1) refs ON refs.scope_id=s.id ORDER BY CASE s.kind WHEN 'attempt' THEN 1 WHEN 'change' THEN 2 ELSE 3 END,s.id`, actionID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		var locked string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM budget_scopes WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
			return err
		}
	}
	return nil
}
