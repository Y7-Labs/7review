package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

type modelBatchSlot struct{ ActionID, IntentDigest string }
type modelBatchSeal struct {
	Version                       int
	RunID, AttemptID, GroupDigest string
	Slots                         []modelBatchSlot
}

func batchSealID(runID, group string) string {
	return "model-batch-" + hash([]byte(runID+"\x00"+group))
}

func checkModelBatchStorage(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT j.model_batch_id,b.manifest FROM budget_jobs j LEFT JOIN budget_model_batches b ON b.id=j.model_batch_id LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

func (a *Authority) readModelBatchSealTx(ctx context.Context, tx *sql.Tx, runID string) (*modelBatchSeal, error) {
	var id sql.NullString
	var attemptID string
	var orphan bool
	err := tx.QueryRowContext(ctx, `SELECT j.model_batch_id,j.attempt_id,EXISTS(SELECT 1 FROM budget_model_batches b WHERE b.run_id=j.run_id) FROM budget_jobs j JOIN budget_inbox i USING(run_id) WHERE j.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3`, runID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&id, &attemptID, &orphan)
	if err != nil {
		return nil, err
	}
	if !id.Valid {
		if orphan {
			return nil, errors.New("batch seal lost its job binding")
		}
		return nil, nil
	}
	var raw []byte
	var digest, group string
	if err = tx.QueryRowContext(ctx, `SELECT manifest,manifest_digest,group_digest FROM budget_model_batches WHERE id=$1 AND run_id=$2 AND attempt_id=$3`, id.String, runID, attemptID).Scan(&raw, &digest, &group); err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 || hash(raw) != digest || id.String != batchSealID(runID, group) {
		return nil, errors.New("batch seal integrity failure")
	}
	var seal modelBatchSeal
	if err = json.Unmarshal(raw, &seal); err != nil {
		return nil, err
	}
	if seal.Version != 1 || seal.RunID != runID || seal.AttemptID != attemptID || seal.GroupDigest != group || len(seal.Slots) < 1 || len(seal.Slots) > 128 {
		return nil, errors.New("batch seal attribution failure")
	}
	seen := map[string]bool{}
	var job *review.JobClaim
	size := 0
	for index, slot := range seal.Slots {
		if seen[slot.ActionID] {
			return nil, errors.New("duplicate sealed batch slot")
		}
		seen[slot.ActionID] = true
		i, _, err := a.storedAction(ctx, tx, slot.ActionID)
		if err != nil {
			return nil, err
		}
		request, err := json.Marshal(i)
		if err != nil {
			return nil, err
		}
		size += len(request)
		if size > 64<<20 || hash(request) != slot.IntentDigest || i.Job == nil || i.Job.RunID != runID || i.Attempt.AttemptID != attemptID || i.Invocation != "complete" {
			return nil, errors.New("sealed batch action changed")
		}
		if job == nil {
			job = i.Job
		} else if *job != *i.Job {
			return nil, errors.New("sealed batch epochs differ")
		}
		cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
		if err != nil {
			return nil, err
		}
		key, err := review.ModelCheckpointGroupDigest(i.ModelCheckpoint)
		if err != nil {
			return nil, err
		}
		if key != group || cp.Cursor.Phase != review.ModelReviewBatches || cp.Cursor.BatchIndex != index || cp.Cursor.BatchCount != len(seal.Slots) || cp.Validation == nil || cp.Source.Diff == nil {
			return nil, errors.New("sealed batch is incomplete or changed")
		}
	}
	return &seal, nil
}

func (a *Authority) guardModelBatchTx(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) error {
	if i.Job == nil {
		return nil
	}
	seal, err := a.readModelBatchSealTx(ctx, tx, i.Job.RunID)
	if err != nil || seal == nil {
		return err
	}
	for _, slot := range seal.Slots {
		if slot.ActionID != i.ActionID {
			continue
		}
		original, _, err := a.storedAction(ctx, tx, i.ActionID)
		if err != nil {
			return err
		}
		// Reclaim may rotate the current job epoch; the request remains the
		// original immutable intent, with only execution ownership replaced.
		claim := *i.Job
		claim.Token = original.Job.Token
		i.Job = &claim
		raw, err := json.Marshal(i)
		if err != nil {
			return err
		}
		if hash(raw) != slot.IntentDigest {
			return errors.New("dispatch changed sealed batch slot")
		}
		return nil
	}
	return a.guardBatchContinuationTx(ctx, tx, i)
}

// Only exact typed children of checked reductions may be nonmembers. This also
// covers their atomic creation before link insertion.
func (a *Authority) guardBatchContinuationTx(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) error {
	var confined bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(recovery_action_id=$2,false) FROM budget_jobs WHERE run_id=$1`, i.Job.RunID, i.ActionID).Scan(&confined); err != nil {
		return err
	}
	if !confined {
		return errors.New("action is outside the sealed model batch")
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.id,min(x.action_id) FROM budget_model_reductions m JOIN budget_model_reduction_inputs x ON x.reduction_id=m.id WHERE m.run_id=$1 AND m.attempt_id=$2 GROUP BY m.id LIMIT 129`, i.Job.RunID, i.Attempt.AttemptID)
	if err != nil {
		return err
	}
	var parentID, actionID string
	count := 0
	for rows.Next() {
		var id, action string
		if err = rows.Scan(&id, &action); err != nil {
			rows.Close()
			return err
		}
		count++
		if continuationKey(id) == i.ActionID {
			parentID, actionID = id, action
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count > 128 || parentID == "" {
		return errors.New("action has no bounded sealed-batch continuation proof")
	}
	parent, err := a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return err
	}
	if err = a.checkReductionProofTx(ctx, tx, parentID, parent); err != nil {
		return err
	}
	i.ActionID, i.Job = "", nil
	if parent.State == review.ModelReductionNeedsTools {
		record, err := a.readToolEvidenceTx(ctx, tx, parentID, parent)
		if err != nil {
			return err
		}
		var current bool
		if err = tx.QueryRowContext(ctx, `SELECT tool_observation_id=$2 FROM budget_jobs WHERE run_id=$1`, parent.Source.Run.ID, toolObservationKey(parentID)).Scan(&current); err != nil {
			return err
		}
		if !current {
			return errors.New("sealed tool child lost its observation cursor")
		}
		return validateToolContinuationPlan(orchestrator.ModelContinuationPlan{Version: 1, ParentGroupDigest: parent.GroupDigest, ParentInputDigest: parent.InputDigest, Intent: i}, parent, record.Artifact)
	}
	return validateContinuationPlan(orchestrator.ModelContinuationPlan{Version: 1, ParentGroupDigest: parent.GroupDigest, ParentInputDigest: parent.InputDigest, Intent: i}, parent)
}
