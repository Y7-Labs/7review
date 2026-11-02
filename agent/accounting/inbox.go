package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

// InboxItem acknowledges durable acceptance, not permission to dispatch.
// Claimed work is never automatically retried: its effects may be uncertain.
type InboxItem struct {
	ValidatedReductions  int64
	PendingContinuations int64
	BlockedReductions    int64
	PreparedActions      int64
	created              bool
	AttemptID            string
	JobPhase             string
	RecordedUnvalidated  int64
	UnresolvedActions    int64
	RunID                string
	Request              review.Request
	State                string
	Deadline             time.Time
	ClaimToken           string
}

var ErrInboxConflict = errors.New("durable inbox delivery payload conflict")

type InboxHealth struct {
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Uncertain int64 `json:"uncertain"`
}

func (a *Authority) CheckInbox(ctx context.Context) (*InboxHealth, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, table := range []string{"budget_inbox", "budget_inbox_conflicts", "budget_jobs", "budget_action_results", "budget_aggregates", "budget_action_leases", "budget_model_reductions", "budget_model_reduction_inputs", "budget_model_continuations"} {
		if err = requireRelation(ctx, tx, table); err != nil {
			return nil, err
		}
	}
	status := &InboxHealth{}
	if err = checkModelCompletionStorage(ctx, tx); err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE state='pending' AND deadline>now()),count(*) FILTER (WHERE state='running' AND deadline>now()),count(*) FILTER (WHERE state='uncertain' OR (state='running' AND deadline<=now())) FROM budget_inbox WHERE boundary_id=$1 AND project_id=$2`, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&status.Pending, &status.Running, &status.Uncertain)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return status, nil
}

func (a *Authority) AcceptInbox(ctx context.Context, req review.Request, timeout time.Duration) (*InboxItem, error) {
	if a.cfg.RequireServiceJobs {
		return nil, errors.New("production service acceptance requires a complete aggregate")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := a.acceptInboxTx(ctx, tx, req, timeout)
	if err != nil && !errors.Is(err, ErrInboxConflict) {
		return nil, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return nil, commitErr
	}
	return item, err
}

func (a *Authority) acceptInboxTx(ctx context.Context, tx *sql.Tx, req review.Request, timeout time.Duration) (*InboxItem, error) {
	if req.Provider == "" || req.ProjectID == "" || (req.ChangeID == "" && req.MRIID <= 0) || timeout <= 0 || timeout > 24*time.Hour {
		return nil, errors.New("invalid durable inbox request")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	fingerprint := hash(raw)
	nonce, err := review.NewOpaqueID("inbox-")
	if err != nil {
		return nil, err
	}
	delivery := req.DeliveryID
	if delivery == "" {
		delivery = nonce
	}
	keyRaw, _ := json.Marshal([]string{a.cfg.BoundaryID, a.cfg.ProjectID, req.Provider, delivery})
	key := hash(keyRaw)
	runID, err := review.NewOpaqueID("run-")
	if err != nil {
		return nil, err
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO budget_inbox(id,boundary_id,project_id,run_id,fingerprint,request,state,deadline) VALUES($1,$2,$3,$4,$5,$6,'pending',clock_timestamp()+$7*interval '1 millisecond') ON CONFLICT(id) DO NOTHING`, key, a.cfg.BoundaryID, a.cfg.ProjectID, runID, fingerprint, raw, timeout.Milliseconds())
	if err != nil {
		return nil, err
	}
	n, err := inserted.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 1 {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_jobs(run_id,phase) VALUES($1,'accepted')`, runID); err != nil {
			return nil, err
		}
	}
	var stored string
	item := &InboxItem{created: n == 1}
	if err = tx.QueryRowContext(ctx, `SELECT run_id,fingerprint,CASE WHEN deadline<=clock_timestamp() AND state='pending' THEN 'expired' WHEN deadline<=clock_timestamp() AND state='running' THEN 'uncertain' ELSE state END,deadline FROM budget_inbox WHERE id=$1 FOR UPDATE`, key).Scan(&item.RunID, &stored, &item.State, &item.Deadline); err != nil {
		return nil, err
	}
	if stored != fingerprint {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_inbox_conflicts(id,inbox_id,fingerprint) VALUES($1,$2,$3)`, nonce, key, fingerprint); err != nil {
			return nil, err
		}
		return nil, ErrInboxConflict
	}
	item.Request = req
	var jobID string
	if err = tx.QueryRowContext(ctx, `SELECT run_id FROM budget_jobs WHERE run_id=$1`, item.RunID).Scan(&jobID); err != nil {
		return nil, errors.New("accepted inbox job missing; recovery required")
	}
	return item, nil
}

// LookupInbox exposes acceptance status without disclosing stored payload or claim credentials.
func (a *Authority) LookupInbox(ctx context.Context, runID string) (*InboxItem, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := a.lookupInboxTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (a *Authority) lookupInboxTx(ctx context.Context, tx *sql.Tx, runID string) (item *InboxItem, err error) {
	item = &InboxItem{}
	err = tx.QueryRowContext(ctx, `SELECT i.run_id,
CASE WHEN i.deadline<=clock_timestamp() AND i.state='pending' THEN 'expired'
WHEN i.deadline<=clock_timestamp() AND i.state='running' THEN 'uncertain' ELSE i.state END,
i.deadline,coalesce(j.attempt_id,''),j.phase,
(SELECT count(*) FROM budget_action_results r WHERE r.run_id=j.run_id),
(SELECT count(*) FROM budget_actions a WHERE a.attempt_id=j.attempt_id AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=j.run_id AND a.state IN ('sending','uncertain')),
(SELECT count(*) FROM budget_actions a WHERE a.attempt_id=j.attempt_id AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=j.run_id AND a.state='prepared'),
(SELECT count(*) FROM budget_model_reductions m WHERE m.run_id=j.run_id AND m.state='validated'),
(SELECT count(*) FROM budget_model_reductions m WHERE m.run_id=j.run_id
AND m.state IN ('needs_tools','needs_findings_repair','needs_coverage_repair')
AND NOT EXISTS(SELECT 1 FROM budget_model_continuations c WHERE c.parent_reduction_id=m.id)),
(SELECT count(*) FROM budget_model_reductions m WHERE m.run_id=j.run_id AND m.state='blocked')
FROM budget_inbox i JOIN budget_jobs j ON j.run_id=i.run_id
WHERE i.boundary_id=$1 AND i.project_id=$2 AND i.run_id=$3`, a.cfg.BoundaryID, a.cfg.ProjectID, runID).Scan(&item.RunID, &item.State, &item.Deadline, &item.AttemptID, &item.JobPhase, &item.RecordedUnvalidated, &item.UnresolvedActions, &item.PreparedActions, &item.ValidatedReductions, &item.PendingContinuations, &item.BlockedReductions)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (a *Authority) ClaimInbox(ctx context.Context) (*InboxItem, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Expiration records uncertainty without granting a new execution window.
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET state=CASE WHEN state='running' THEN 'uncertain' ELSE 'expired' END,updated_at=clock_timestamp() WHERE boundary_id=$1 AND project_id=$2 AND state IN ('pending','running') AND deadline<=clock_timestamp()`, a.cfg.BoundaryID, a.cfg.ProjectID); err != nil {
		return nil, err
	}
	item := &InboxItem{}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT i.run_id,i.request,i.deadline FROM budget_inbox i WHERE i.boundary_id=$1 AND i.project_id=$2 AND i.state='pending' AND i.deadline>clock_timestamp() AND EXISTS (SELECT 1 FROM budget_jobs j WHERE j.run_id=i.run_id AND j.phase IN ('accepted','admitted')) AND (NOT $3 OR EXISTS (SELECT 1 FROM budget_aggregates g WHERE g.run_id=i.run_id)) ORDER BY i.created_at,i.id LIMIT 1 FOR UPDATE OF i SKIP LOCKED`, a.cfg.BoundaryID, a.cfg.ProjectID, a.cfg.RequireServiceJobs).Scan(&item.RunID, &raw, &item.Deadline)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &item.Request); err != nil {
		return nil, err
	}
	item.ClaimToken, err = review.NewOpaqueID("claim-")
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET state='running',claim_token=$2,updated_at=clock_timestamp() WHERE run_id=$1`, item.RunID, item.ClaimToken); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE budget_jobs SET phase=CASE WHEN phase='accepted' THEN 'acquiring' ELSE phase END,updated_at=clock_timestamp() WHERE run_id=$1 AND phase IN ('accepted','admitted')`, item.RunID)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, errors.New("inbox job is not claimable")
	}
	item.State = "running"
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

func (a *Authority) FinishInbox(ctx context.Context, runID, token string, success bool) error {
	tx, err := a.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state := "failed"
	if success {
		state = "completed"
	}
	result, err := tx.ExecContext(ctx, `UPDATE budget_inbox SET state=$5,updated_at=clock_timestamp() WHERE boundary_id=$1 AND project_id=$2 AND run_id=$3 AND claim_token=$4 AND state='running' AND deadline>clock_timestamp()`, a.cfg.BoundaryID, a.cfg.ProjectID, runID, token, state)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("inbox completion is unowned, expired or already terminal")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET phase='finished',updated_at=clock_timestamp() WHERE run_id=$1`, runID); err != nil {
		return err
	}
	return tx.Commit()
}
