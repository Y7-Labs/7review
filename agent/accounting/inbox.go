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
	RunID      string
	Request    review.Request
	State      string
	Deadline   time.Time
	ClaimToken string
}

func (a *Authority) AcceptInbox(ctx context.Context, req review.Request, timeout time.Duration) (*InboxItem, error) {
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
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_inbox(id,boundary_id,project_id,run_id,fingerprint,request,state,deadline) VALUES($1,$2,$3,$4,$5,$6,'pending',clock_timestamp()+$7*interval '1 millisecond') ON CONFLICT(id) DO NOTHING`, key, a.cfg.BoundaryID, a.cfg.ProjectID, runID, fingerprint, raw, timeout.Milliseconds()); err != nil {
		return nil, err
	}
	var stored string
	item := &InboxItem{}
	if err = tx.QueryRowContext(ctx, `SELECT run_id,fingerprint,CASE WHEN deadline<=clock_timestamp() AND state='pending' THEN 'expired' WHEN deadline<=clock_timestamp() AND state='running' THEN 'uncertain' ELSE state END,deadline FROM budget_inbox WHERE id=$1 FOR UPDATE`, key).Scan(&item.RunID, &stored, &item.State, &item.Deadline); err != nil {
		return nil, err
	}
	if stored != fingerprint {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_inbox_conflicts(id,inbox_id,fingerprint) VALUES($1,$2,$3)`, nonce, key, fingerprint); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, errors.New("durable inbox delivery payload conflict")
	}
	item.Request = req
	if err = tx.Commit(); err != nil {
		return nil, err
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
	item := &InboxItem{}
	err = tx.QueryRowContext(ctx, `SELECT run_id,CASE WHEN deadline<=clock_timestamp() AND state='pending' THEN 'expired' WHEN deadline<=clock_timestamp() AND state='running' THEN 'uncertain' ELSE state END,deadline FROM budget_inbox WHERE boundary_id=$1 AND project_id=$2 AND run_id=$3`, a.cfg.BoundaryID, a.cfg.ProjectID, runID).Scan(&item.RunID, &item.State, &item.Deadline)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
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
	err = tx.QueryRowContext(ctx, `SELECT run_id,request,deadline FROM budget_inbox WHERE boundary_id=$1 AND project_id=$2 AND state='pending' AND deadline>clock_timestamp() ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&item.RunID, &raw, &item.Deadline)
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
	return tx.Commit()
}
