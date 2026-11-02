package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Y4NN777/7review/agent/orchestrator"
)

// RecoverActionResults replays only local indexing of committed responses. It
// grants neither dispatch, evidence-validation nor financial-settlement authority.
// One job and at most limit actions are reduced in one transaction.
func (a *Authority) RecoverActionResults(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 128 {
		return 0, errors.New("invalid action recovery batch bound")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var runID, attemptID string
	// Attribution comes from the immutable sending intent, not a grant that may
	// have been revoked or disabled by migration. This never permits resending.
	err = tx.QueryRowContext(ctx, `SELECT j.run_id,j.attempt_id FROM budget_jobs j JOIN budget_inbox i ON i.run_id=j.run_id WHERE i.boundary_id=$1 AND i.project_id=$2 AND EXISTS (SELECT 1 FROM budget_actions a WHERE a.attempt_id=j.attempt_id AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=j.run_id AND a.state='result_recorded' AND NOT EXISTS (SELECT 1 FROM budget_action_results r WHERE r.action_id=a.id)) ORDER BY j.run_id LIMIT 1 FOR UPDATE OF j SKIP LOCKED`, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&runID, &attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, tx.Commit()
	}
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.request_digest,a.outcome FROM budget_actions a WHERE a.attempt_id=$1 AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=$3 AND a.state='result_recorded' AND NOT EXISTS (SELECT 1 FROM budget_action_results r WHERE r.action_id=a.id) ORDER BY a.created_at,a.id LIMIT $2`, attemptID, limit, runID)
	if err != nil {
		return 0, err
	}
	type result struct {
		id, digest string
		raw        []byte
		outcome    orchestrator.DispatchOutcome
	}
	var results []result
	bytes := 0
	for rows.Next() {
		var r result
		if err = rows.Scan(&r.id, &r.digest, &r.raw); err != nil {
			rows.Close()
			return 0, err
		}
		if len(r.raw) > 16<<20 {
			rows.Close()
			return 0, errors.New("recorded action outcome exceeds recovery bound")
		}
		if bytes+len(r.raw) > 16<<20 {
			break
		}
		bytes += len(r.raw)
		if err = json.Unmarshal(r.raw, &r.outcome); err != nil {
			rows.Close()
			return 0, err
		}
		if !r.outcome.ResponseReceived || hash(r.outcome.Response) != r.outcome.ResponseDigest || !canonicalDigest(r.digest) {
			rows.Close()
			return 0, errors.New("recorded action outcome integrity failure")
		}
		results = append(results, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, r := range results {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_action_results(action_id,run_id,request_digest,response_digest,outcome,status) VALUES($1,$2,$3,$4,$5,'recorded_unvalidated')`, r.id, runID, r.digest, r.outcome.ResponseDigest, r.raw); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(results), nil
}
