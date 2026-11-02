package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

var ErrOperatorAttemptNotFound = errors.New("operator attempt not found")
var ErrOperatorAttemptInvalid = errors.New("exact attempt ID required")

// OperatorAttemptView is a durable status projection, not a dispatch permit,
// validated assessment or mutable command version.
type OperatorAttemptView struct {
	AttemptID  string    `json:"attempt_id"`
	RunID      string    `json:"run_id"`
	Revision   string    `json:"revision"`
	InboxState string    `json:"inbox_state"`
	JobPhase   string    `json:"job_phase"`
	Current    bool      `json:"current"`
	Active     bool      `json:"active"`
	Deadline   time.Time `json:"deadline"`
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
	var bindingRaw []byte
	var digest string
	err = tx.QueryRowContext(ctx, `SELECT j.attempt_id,j.run_id,a.binding,a.binding_digest,a.active,
coalesce(h.current_attempt_id=a.id,false),j.phase,i.deadline,
CASE WHEN i.deadline<=clock_timestamp() AND i.state='pending' THEN 'expired'
WHEN i.deadline<=clock_timestamp() AND i.state='running' THEN 'uncertain' ELSE i.state END
FROM budget_jobs j JOIN budget_inbox i ON i.run_id=j.run_id
JOIN budget_attempts a ON a.id=j.attempt_id LEFT JOIN budget_heads h ON h.id=a.tracking_id
WHERE j.attempt_id=$1 AND i.boundary_id=$2 AND i.project_id=$3`, attemptID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&v.AttemptID, &v.RunID, &bindingRaw, &digest, &v.Active, &v.Current, &v.JobPhase, &v.Deadline, &v.InboxState)
	if errors.Is(err, sql.ErrNoRows) {
		return OperatorAttemptView{}, ErrOperatorAttemptNotFound
	}
	if err != nil {
		return OperatorAttemptView{}, err
	}
	var b attemptBinding
	if len(bindingRaw) > 1<<20 || hash(bindingRaw) != digest || json.Unmarshal(bindingRaw, &b) != nil || b.Attempt.AttemptID != attemptID || b.ProjectID != a.cfg.ProjectID {
		return OperatorAttemptView{}, ErrIntegrity
	}
	if err = a.cfg.Operators.Authorize(actor, a.cfg.BoundaryID, a.cfg.ProjectID, b.Attempt.Change, review.OperatorRoleOperator, review.OperatorRoleReviewer); err != nil {
		return OperatorAttemptView{}, err
	}
	aggregate, err := a.readAggregateTx(ctx, tx, v.RunID)
	if err != nil {
		return OperatorAttemptView{}, err
	}
	if !reflect.DeepEqual(aggregate.Receipt.Attempt, b.Attempt) || !reflect.DeepEqual(aggregate.Receipt.Execution, b.Execution) {
		return OperatorAttemptView{}, ErrIntegrity
	}
	v.Revision = b.Attempt.Snapshot.AnalysisRevision()
	if err = tx.Commit(); err != nil {
		return OperatorAttemptView{}, err
	}
	return v, nil
}
