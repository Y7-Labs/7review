package accounting

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

// PrepareModelBatch reserves a complete frozen initial batch set atomically.
// It grants no lease, sending permit, or post-progress recovery authority.
func (a *Authority) PrepareModelBatch(ctx context.Context, intents []orchestrator.DispatchIntent) ([]orchestrator.PreparedAction, error) {
	if len(intents) < 1 || len(intents) > 128 {
		return nil, errors.New("invalid model batch bound")
	}
	bytes := 0
	for _, i := range intents {
		if len(i.Payload) > 1<<20 || len(i.ModelCheckpoint) > review.MaxModelCheckpointBytes {
			return nil, errors.New("model batch input exceeds storage bound")
		}
		bytes += len(i.Payload) + len(i.ModelCheckpoint)
		if bytes > 64<<20 {
			return nil, errors.New("model batch inputs exceed storage bound")
		}
	}
	raw, err := json.Marshal(intents)
	if err != nil || len(raw) > 64<<20 {
		return nil, errors.New("model batch exceeds immutable storage bound")
	}
	var frozen []orchestrator.DispatchIntent
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	requests := make(map[string]string, len(frozen))
	group := ""
	for index, i := range frozen {
		if i.Job == nil || i.Invocation != "complete" || !validID(i.ActionID) {
			return nil, errors.New("batch requires canonical complete actions")
		}
		if _, duplicate := requests[i.ActionID]; duplicate {
			return nil, errors.New("duplicate batch action identity")
		}
		if *i.Job != *frozen[0].Job {
			return nil, errors.New("batch changed job ownership")
		}
		cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
		if err != nil {
			return nil, err
		}
		if cp.Cursor.Phase != review.ModelReviewBatches || cp.Cursor.BatchCount != len(frozen) || cp.Cursor.BatchIndex != index || cp.Validation == nil || cp.Source.Diff == nil {
			return nil, errors.New("batch is incomplete, unordered or unqualified")
		}
		if err = orchestrator.ValidateModelCheckpoint(i); err != nil {
			return nil, err
		}
		key, err := review.ModelCheckpointGroupDigest(i.ModelCheckpoint)
		if err != nil {
			return nil, err
		}
		if index == 0 {
			group = key
		} else if key != group {
			return nil, errors.New("batch changed frozen source or continuation inputs")
		}
		request, err := json.Marshal(i)
		if err != nil {
			return nil, err
		}
		requests[i.ActionID] = hash(request)
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	first := frozen[0]
	if _, _, err = a.lockJob(ctx, tx, first.Job); err != nil {
		return nil, err
	}
	var clean bool
	if err = tx.QueryRowContext(ctx, `SELECT phase='admitted' AND dispatch_allowed AND recovery_action_id IS NULL AND attempt_id=$2 FROM budget_jobs WHERE run_id=$1`, first.Job.RunID, first.Attempt.AttemptID).Scan(&clean); err != nil {
		return nil, err
	}
	if !clean {
		return nil, errors.New("batch preparation requires an unconfined pre-effect job")
	}

	// A retry must match the whole existing set. Never adopt a partially written
	// legacy batch or unrelated prepared work as an atomic plan.
	rows, err := tx.QueryContext(ctx, `SELECT id,intent_digest,state FROM budget_actions WHERE attempt_id=$1 AND convert_from(request,'UTF8')::jsonb #>> '{Job,RunID}'=$2 LIMIT 129`, first.Attempt.AttemptID, first.Job.RunID)
	if err != nil {
		return nil, err
	}
	existing := 0
	for rows.Next() {
		var id, digest, state string
		if err = rows.Scan(&id, &digest, &state); err != nil {
			rows.Close()
			return nil, err
		}
		if requests[id] != digest || state != "prepared" || existing >= len(frozen) {
			rows.Close()
			return nil, errors.New("existing actions conflict with complete batch plan")
		}
		existing++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if existing != 0 && existing != len(frozen) {
		return nil, errors.New("partial legacy batch cannot be adopted")
	}
	prepared := make([]orchestrator.PreparedAction, 0, len(frozen))
	for _, i := range frozen {
		p, err := a.reserveActionTx(ctx, tx, i, true)
		if err != nil {
			return nil, err
		}
		if p.AlreadyStarted {
			return nil, errors.New("batch contains an already started action")
		}
		prepared = append(prepared, orchestrator.PreparedAction{ID: p.ID, RequestDigest: p.RequestDigest, Fence: p.Fence})
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return prepared, nil
}
