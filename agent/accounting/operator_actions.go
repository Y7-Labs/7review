package accounting

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func (a *Authority) OperatorActions(ctx context.Context, actor review.OperatorActor, attemptID string) (review.OperatorActionsView, error) {
	view := review.OperatorActionsView{SchemaVersion: 1, AttemptID: attemptID, Actions: []review.OperatorActionView{}}
	if err := a.cfg.Operators.ValidateActor(actor); err != nil {
		return review.OperatorActionsView{}, err
	}
	if !validID(attemptID) {
		return review.OperatorActionsView{}, ErrOperatorAttemptInvalid
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return review.OperatorActionsView{}, err
	}
	defer tx.Rollback()
	record, err := a.operatorAttemptForRolesTx(ctx, tx, actor, attemptID, false, review.OperatorRoleOperator)
	if err != nil {
		return review.OperatorActionsView{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.request_digest,a.intent_digest,a.request,a.quote,a.upper_amount,a.state,a.settled,a.actual_amount,a.receipt_id,a.integrity_incident,a.created_at,r.payload,r.payload_digest
FROM budget_actions a LEFT JOIN budget_receipts r ON r.id=a.receipt_id
WHERE a.attempt_id=$1 ORDER BY a.created_at,a.id LIMIT $2`, attemptID, review.MaxOperatorActions+1)
	if err != nil {
		return review.OperatorActionsView{}, err
	}
	type storedAction struct {
		id, requestDigest, intentDigest, state string
		request, quote, upper                  []byte
		settled, incident                      bool
		actual, receiptID, receipt             sql.NullString
		receiptDigest                          sql.NullString
		createdAt                              time.Time
	}
	var stored []storedAction
	for rows.Next() {
		var item storedAction
		if err = rows.Scan(&item.id, &item.requestDigest, &item.intentDigest, &item.request, &item.quote, &item.upper, &item.state, &item.settled, &item.actual, &item.receiptID, &item.incident, &item.createdAt, &item.receipt, &item.receiptDigest); err != nil {
			rows.Close()
			return review.OperatorActionsView{}, err
		}
		stored = append(stored, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return review.OperatorActionsView{}, err
	}
	if err = rows.Close(); err != nil {
		return review.OperatorActionsView{}, err
	}
	if len(stored) > review.MaxOperatorActions {
		return review.OperatorActionsView{}, errors.New("operator action projection exceeds bound")
	}
	for _, item := range stored {
		action, err := decodeOperatorAction(item.id, record.binding, item.requestDigest, item.intentDigest, item.request, item.quote, item.upper, item.state, item.settled, item.actual, item.receiptID, item.incident, item.createdAt, item.receipt, item.receiptDigest)
		if err != nil {
			return review.OperatorActionsView{}, err
		}
		if item.state == "not_executed" {
			if _, err = verifyUnusedReleaseTx(ctx, tx, item.id); err != nil {
				return review.OperatorActionsView{}, err
			}
		}
		view.Actions = append(view.Actions, action)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		return review.OperatorActionsView{}, ErrIntegrity
	}
	validated, err := review.DecodeOperatorActionsView(encoded, attemptID)
	if err != nil {
		return review.OperatorActionsView{}, fmt.Errorf("%w: operator projection contract mismatch", ErrIntegrity)
	}
	if err = tx.Commit(); err != nil {
		return review.OperatorActionsView{}, err
	}
	return validated, nil
}

func decodeOperatorAction(id string, binding attemptBinding, requestDigest, intentDigest string, request, quoteRaw, upperRaw []byte, state string, settled bool, actualRaw, receiptID sql.NullString, incident bool, createdAt time.Time, receiptRaw, receiptDigest sql.NullString) (review.OperatorActionView, error) {
	var intent orchestrator.DispatchIntent
	var quote Quote
	var upper review.ResourceLimitScope
	if len(request) > 24<<20 || len(quoteRaw) > 1<<20 || len(upperRaw) > 1<<20 || hash(request) != intentDigest || json.Unmarshal(request, &intent) != nil || json.Unmarshal(quoteRaw, &quote) != nil || json.Unmarshal(upperRaw, &upper) != nil {
		return review.OperatorActionView{}, ErrIntegrity
	}
	payloadDigest := sha256.Sum256(intent.Payload)
	qualificationRaw, err := json.Marshal(quote.Qualification)
	if err != nil {
		return review.OperatorActionView{}, ErrIntegrity
	}
	expectedUpper := review.ResourceLimitScope{ModelCalls: 1, InputTokens: quote.InputTokens, OutputTokens: quote.OutputTokens, MoneyMicro: quote.MoneyMicro, ActiveMS: quote.ActiveMS}
	if intent.ActionID != id || !reflect.DeepEqual(intent.Attempt, binding.Attempt) || !reflect.DeepEqual(intent.Execution, binding.Execution) || !reflect.DeepEqual(intent.Attestation, binding.Attestation) || !reflect.DeepEqual(intent.PolicyLimits, binding.PolicyLimits) || !reflect.DeepEqual(intent.OperatorCeilings, binding.OperatorCeilings) {
		return review.OperatorActionView{}, fmt.Errorf("%w: stored action binding mismatch", ErrIntegrity)
	}
	if intent.RequestDigest != requestDigest || requestDigest != "sha256:"+hex.EncodeToString(payloadDigest[:]) {
		return review.OperatorActionView{}, fmt.Errorf("%w: stored action request mismatch", ErrIntegrity)
	}
	if quote.RequestDigest != requestDigest || quote.Qualification.Provider != intent.Provider || quote.Qualification.Model != intent.Model || quote.Currency == "" || quote.Currency != quote.Qualification.Currency || quote.RouteVersion != quote.Qualification.Version || quote.EvidenceRef != quote.Qualification.EvidenceRef || quote.QualificationDigest != hash(qualificationRaw) {
		return review.OperatorActionView{}, fmt.Errorf("%w: stored action route mismatch", ErrIntegrity)
	}
	if !reflect.DeepEqual(upper, expectedUpper) {
		return review.OperatorActionView{}, fmt.Errorf("%w: stored action upper bound mismatch", ErrIntegrity)
	}
	view := review.OperatorActionView{ActionID: id, State: state, Provider: intent.Provider, Model: intent.Model, Role: intent.Role, Step: intent.Step, Currency: quote.Currency, RouteVersion: quote.RouteVersion, Upper: usageActual(upper), Settled: settled, IntegrityIncident: incident, CreatedAt: createdAt}
	if !settled {
		if actualRaw.Valid || receiptID.Valid || receiptRaw.Valid || receiptDigest.Valid {
			return review.OperatorActionView{}, ErrIntegrity
		}
		return view, nil
	}
	if !actualRaw.Valid || !receiptID.Valid || !receiptRaw.Valid || !receiptDigest.Valid || hash([]byte(receiptRaw.String)) != receiptDigest.String {
		return review.OperatorActionView{}, ErrIntegrity
	}
	var actual review.ResourceLimitScope
	var receipt FinalReceipt
	if json.Unmarshal([]byte(actualRaw.String), &actual) != nil || json.Unmarshal([]byte(receiptRaw.String), &receipt) != nil || validateFinalReceipt(receipt) != nil || receipt.ReceiptID != receiptID.String || receipt.ActionID != id || receipt.RequestDigest != requestDigest || receipt.Provider != intent.Provider || receipt.Model != intent.Model || receipt.Currency != quote.Currency || !reflect.DeepEqual(receipt.Actual, actual) {
		return review.OperatorActionView{}, ErrIntegrity
	}
	actualView := usageActual(actual)
	view.Actual = &actualView
	view.ReceiptID = receipt.ReceiptID
	view.EvidenceRef = receipt.EvidenceRef
	return view, nil
}

func usageActual(amount review.ResourceLimitScope) review.ModelUsageActual {
	return review.ModelUsageActual{InputTokens: amount.InputTokens, OutputTokens: amount.OutputTokens, MoneyMicro: amount.MoneyMicro, ActiveMS: amount.ActiveMS}
}
