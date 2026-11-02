package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/Y4NN777/7review/agent/review"
)

var ErrOperatorAttemptNotFound = errors.New("operator attempt not found")
var ErrOperatorAttemptInvalid = errors.New("exact attempt ID required")

type OperatorAttemptView = review.OperatorAttemptView

type operatorAttemptRecord struct {
	view          OperatorAttemptView
	binding       attemptBinding
	bindingDigest string
	tracking      sql.NullString
	cancelID      sql.NullString
}

func (a *Authority) OperatorAttempt(ctx context.Context, actor review.OperatorActor, attemptID string) (OperatorAttemptView, error) {
	var v OperatorAttemptView
	if err := a.cfg.Operators.ValidateActor(actor); err != nil {
		return v, err
	}
	if !validID(attemptID) {
		return v, ErrOperatorAttemptInvalid
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	record, err := a.operatorAttemptTx(ctx, tx, actor, attemptID, false)
	if err != nil {
		return v, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM budget_actions WHERE attempt_id=$1 AND state='not_executed'`, attemptID)
	if err != nil {
		return v, err
	}
	var released []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return v, err
		}
		released = append(released, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return v, err
	}
	if err = rows.Close(); err != nil {
		return v, err
	}
	for _, id := range released {
		if _, err = verifyUnusedReleaseTx(ctx, tx, id); err != nil {
			return v, err
		}
	}
	if err = tx.Commit(); err != nil {
		return v, err
	}
	return record.view, nil
}

func (a *Authority) operatorAttemptTx(ctx context.Context, tx *sql.Tx, actor review.OperatorActor, attemptID string, lock bool) (operatorAttemptRecord, error) {
	var record operatorAttemptRecord
	v := &record.view
	var bindingRaw []byte
	var digest string
	query := `SELECT j.attempt_id,j.run_id,a.binding,a.binding_digest,a.active,
coalesce(h.current_attempt_id=a.id,false),j.phase,i.deadline,
CASE WHEN i.deadline<=clock_timestamp() AND i.state='pending' THEN 'expired'
WHEN i.deadline<=clock_timestamp() AND i.state='running' THEN 'uncertain' ELSE i.state END,
j.control_version,a.tracking_id,j.cancel_command_id,
(SELECT count(*) FROM budget_actions x WHERE x.attempt_id=j.attempt_id AND x.state='not_executed'),
(SELECT count(*) FROM budget_actions x WHERE x.attempt_id=j.attempt_id AND NOT x.settled AND x.state<>'not_executed')
FROM budget_inbox i JOIN budget_jobs j ON j.run_id=i.run_id
JOIN budget_attempts a ON a.id=j.attempt_id LEFT JOIN budget_heads h ON h.id=a.tracking_id
WHERE j.attempt_id=$1 AND i.boundary_id=$2 AND i.project_id=$3
AND convert_from(j.receipt,'UTF8')::jsonb->>'Duplicate'='false'`
	if lock {
		query += ` FOR UPDATE OF i,j`
	}
	err := tx.QueryRowContext(ctx, query, attemptID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&v.AttemptID, &v.RunID, &bindingRaw, &digest, &v.Active, &v.Current, &v.JobPhase, &v.Deadline, &v.InboxState, &v.ControlVersion, &record.tracking, &record.cancelID, &v.ReleasedUnusedActions, &v.RetainedModelReservations)
	if errors.Is(err, sql.ErrNoRows) {
		return operatorAttemptRecord{}, ErrOperatorAttemptNotFound
	}
	if err != nil {
		return operatorAttemptRecord{}, err
	}
	var b attemptBinding
	if len(bindingRaw) > 1<<20 || hash(bindingRaw) != digest || json.Unmarshal(bindingRaw, &b) != nil || b.Attempt.AttemptID != attemptID || b.ProjectID != a.cfg.ProjectID {
		return operatorAttemptRecord{}, ErrIntegrity
	}
	if err = a.cfg.Operators.Authorize(actor, a.cfg.BoundaryID, a.cfg.ProjectID, b.Attempt.Change, review.OperatorRoleOperator, review.OperatorRoleReviewer); err != nil {
		return operatorAttemptRecord{}, err
	}
	aggregate, err := a.readAggregateTx(ctx, tx, v.RunID)
	if err != nil {
		return operatorAttemptRecord{}, err
	}
	if !reflect.DeepEqual(aggregate.Receipt.Attempt, b.Attempt) || !reflect.DeepEqual(aggregate.Receipt.Execution, b.Execution) {
		return operatorAttemptRecord{}, ErrIntegrity
	}
	v.Revision = b.Attempt.Snapshot.AnalysisRevision()
	record.binding = b
	record.bindingDigest = digest
	return record, nil
}
