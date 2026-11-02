package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

// RecoverModelReductions commits one local, immutable group artifact and its
// exact input links. It never changes dispatch ownership or creates actions.
func (a *Authority) RecoverModelReductions(ctx context.Context, limit int, reducer orchestrator.ModelResultReducer) (int, error) {
	if limit < 1 || limit > 128 || reducer == nil {
		return 0, errors.New("invalid model reduction bound or adapter")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var runID string
	err = tx.QueryRowContext(ctx, `SELECT j.run_id FROM budget_jobs j JOIN budget_inbox i USING(run_id) WHERE i.boundary_id=$1 AND i.project_id=$2 AND EXISTS(SELECT 1 FROM budget_action_results r JOIN budget_actions a ON a.id=r.action_id WHERE r.run_id=j.run_id AND a.state='result_recorded' AND convert_from(a.request,'UTF8')::jsonb ? 'ModelCheckpoint' AND NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x WHERE x.action_id=a.id)) ORDER BY j.run_id LIMIT 1 FOR UPDATE OF j SKIP LOCKED`, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, tx.Commit()
	}
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.action_id,r.request_digest,r.response_digest,r.outcome,a.outcome FROM budget_action_results r JOIN budget_actions a ON a.id=r.action_id WHERE r.run_id=$1 AND a.state='result_recorded' AND convert_from(a.request,'UTF8')::jsonb ? 'ModelCheckpoint' AND NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x WHERE x.action_id=a.id) ORDER BY a.created_at,a.id LIMIT $2`, runID, limit+1)
	if err != nil {
		return 0, err
	}
	type journal struct {
		id, requestDigest, responseDigest string
		raw                               []byte
	}
	var journaled []journal
	size := 0
	for rows.Next() {
		var r journal
		var original []byte
		if err = rows.Scan(&r.id, &r.requestDigest, &r.responseDigest, &r.raw, &original); err != nil {
			rows.Close()
			return 0, err
		}
		size += len(r.raw)
		if len(journaled) >= limit || size > 64<<20 || !bytes.Equal(r.raw, original) {
			rows.Close()
			return 0, errors.New("model journal exceeds reduction bound or changed recorded outcome")
		}
		journaled = append(journaled, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var group string
	var checkpoint review.ModelCheckpoint
	var inputs []orchestrator.RecordedModelResult
	positions := map[string]int{}
	for _, r := range journaled {
		i, _, err := a.storedAction(ctx, tx, r.id)
		if err != nil {
			return 0, err
		}
		if i.Job == nil || i.Job.RunID != runID || i.RequestDigest != r.requestDigest {
			return 0, errors.New("recorded reduction attribution mismatch")
		}
		var outcome orchestrator.DispatchOutcome
		if err = json.Unmarshal(r.raw, &outcome); err != nil {
			return 0, err
		}
		if !outcome.ResponseReceived || outcome.ResponseDigest != r.responseDigest || hash(outcome.Response) != r.responseDigest {
			return 0, errors.New("recorded reduction response integrity failure")
		}
		key, err := review.ModelCheckpointGroupDigest(i.ModelCheckpoint)
		if err != nil {
			return 0, err
		}
		if group == "" {
			group = key
			checkpoint, err = review.DecodeModelCheckpoint(i.ModelCheckpoint)
			if err != nil {
				return 0, err
			}
		}
		if key != group {
			continue
		}
		cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
		if err != nil {
			return 0, err
		}
		positions[i.ActionID] = cp.Cursor.BatchIndex
		size += len(i.ModelCheckpoint) + len(i.Payload)
		if size > 64<<20 {
			return 0, errors.New("model reduction input exceeds storage bound")
		}
		inputs = append(inputs, orchestrator.RecordedModelResult{Intent: i, Outcome: outcome})
	}
	want := 1
	if checkpoint.Cursor.Phase == review.ModelReviewBatches {
		want = checkpoint.Cursor.BatchCount
	}
	if len(inputs) < want {
		return 0, tx.Commit()
	}
	sort.Slice(inputs, func(i, j int) bool {
		return positions[inputs[i].Intent.ActionID] < positions[inputs[j].Intent.ActionID]
	})
	if checkpoint.Cursor.Phase == review.ModelReviewBatches {
		checkpoint.Cursor.BatchIndex = 0
	}
	var expected []review.ModelResultRef
	expectedRaw := append([]string(nil), checkpoint.RawResponses...)
	for _, input := range inputs {
		expected = append(expected, review.ModelResultRef{ActionID: input.Intent.ActionID, RequestDigest: input.Intent.RequestDigest, ResponseDigest: input.Outcome.ResponseDigest, CheckpointDigest: hash(input.Intent.ModelCheckpoint)})
		expectedRaw = append(expectedRaw, string(input.Outcome.Response))
	}
	artifact, err := invokeModelReducer(ctx, reducer, inputs)
	if err != nil {
		return 0, err
	}
	if err = validateModelReduction(artifact, expected, expectedRaw, group, checkpoint); err != nil {
		return 0, err
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		return 0, err
	}
	if len(raw) > 32<<20 {
		return 0, errors.New("model reduction artifact exceeds storage bound")
	}
	id := "reduction-" + strings.TrimPrefix(hash([]byte(runID+"\x00"+group)), "sha256:")
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_model_reductions(id,run_id,attempt_id,group_digest,input_digest,artifact_digest,artifact,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, runID, artifact.Source.Attempt.AttemptID, group, artifact.InputDigest, hash(raw), raw, artifact.State); err != nil {
		return 0, err
	}
	for _, input := range expected {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_model_reduction_inputs(action_id,reduction_id) VALUES($1,$2)`, input.ActionID, id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return 1, nil
}

func invokeModelReducer(ctx context.Context, reducer orchestrator.ModelResultReducer, inputs []orchestrator.RecordedModelResult) (artifact review.ModelReduction, err error) {
	defer func() {
		if recover() != nil {
			artifact = review.ModelReduction{}
			err = errors.New("local model reducer panicked; retry local reduction only")
		}
	}()
	return reducer.ReduceModelResults(ctx, inputs)
}

func validateModelReduction(artifact review.ModelReduction, refs []review.ModelResultRef, rawResponses []string, group string, cp review.ModelCheckpoint) error {
	equal := func(a, b any) bool {
		x, e := json.Marshal(a)
		y, f := json.Marshal(b)
		return e == nil && f == nil && bytes.Equal(x, y)
	}
	encodedRefs, _ := json.Marshal(refs)
	if artifact.Version != 1 || artifact.GroupDigest != group || artifact.InputDigest != hash(encodedRefs) || !equal(artifact.Inputs, refs) || !equal(artifact.RawResponses, rawResponses) || !equal(artifact.Cursor, cp.Cursor) || cp.Validation == nil || !equal(artifact.Validation, *cp.Validation) {
		return errors.New("local reduction changed its immutable inputs")
	}
	s := artifact.Source
	original := cp.Source
	if !equal(immutableReductionSource(s), immutableReductionSource(original)) || s.Report.Final != "" {
		return errors.New("local reduction changed source authority or final report")
	}
	if (artifact.State == review.ModelReductionValidated || artifact.State == review.ModelReductionNeedsCoverageRepair) && !equal(s.Model.RawResponses, rawResponses) {
		return errors.New("local reduction changed its raw response audit")
	}
	switch artifact.State {
	case review.ModelReductionValidated:
		if artifact.NextCursor != nil || len(artifact.ToolRequests) != 0 || s.Report.Draft == "" {
			return errors.New("invalid validated reduction artifact")
		}
	case review.ModelReductionBlocked:
		if artifact.NextCursor != nil || s.Report.Draft != "" {
			return errors.New("blocked reduction grants continuation")
		}
	case review.ModelReductionNeedsTools, review.ModelReductionNeedsFindingsRepair, review.ModelReductionNeedsCoverageRepair:
		if artifact.NextCursor == nil || s.Report.Draft != "" {
			return errors.New("pending reduction grants a draft or lacks a cursor")
		}
		want := review.ModelToolRound
		if artifact.State == review.ModelReductionNeedsFindingsRepair {
			want = review.ModelRepairFindings
		}
		if artifact.State == review.ModelReductionNeedsCoverageRepair {
			want = review.ModelRepairCoverage
		}
		if artifact.NextCursor.Phase != want {
			return errors.New("reduction proposed a mismatched next phase")
		}
		raw, err := json.Marshal(review.ModelCheckpoint{Version: 1, Cursor: *artifact.NextCursor})
		if err != nil {
			return err
		}
		if _, err = review.DecodeModelCheckpoint(raw); err != nil {
			return err
		}
	default:
		return errors.New("unsupported local reduction state")
	}
	return nil
}

func immutableReductionSource(s review.Source) review.Source {
	s.Model = review.ModelReview{}
	s.SkillCoverage = nil
	s.Findings = nil
	s.HumanCheck = nil
	s.Notes = nil
	s.Questions = nil
	s.InlineComments = nil
	s.Report = review.Report{}
	s.Run.StepProviders = nil
	s.Run.Warnings = nil
	return s
}

func (a *Authority) LookupModelReduction(ctx context.Context, actionID string) (review.ModelReduction, error) {
	var artifact review.ModelReduction
	tx, err := a.begin(ctx)
	if err != nil {
		return artifact, err
	}
	defer tx.Rollback()
	var raw []byte
	var digest, group, inputDigest, state, runID, attemptID string
	err = tx.QueryRowContext(ctx, `SELECT m.artifact,m.artifact_digest,m.group_digest,m.input_digest,m.state,m.run_id,m.attempt_id FROM budget_model_reductions m JOIN budget_model_reduction_inputs x ON x.reduction_id=m.id JOIN budget_inbox i ON i.run_id=m.run_id WHERE x.action_id=$1 AND i.boundary_id=$2 AND i.project_id=$3`, actionID, a.cfg.BoundaryID, a.cfg.ProjectID).Scan(&raw, &digest, &group, &inputDigest, &state, &runID, &attemptID)
	if err != nil {
		return artifact, err
	}
	if len(raw) > 32<<20 || hash(raw) != digest {
		return artifact, errors.New("model reduction artifact integrity failure")
	}
	if err = json.Unmarshal(raw, &artifact); err != nil {
		return review.ModelReduction{}, err
	}
	refs, _ := json.Marshal(artifact.Inputs)
	if artifact.Version != 1 || artifact.GroupDigest != group || artifact.InputDigest != inputDigest || hash(refs) != inputDigest || string(artifact.State) != state || artifact.Source.Run.ID != runID || artifact.Source.Attempt.AttemptID != attemptID {
		return review.ModelReduction{}, errors.New("model reduction artifact attribution failure")
	}
	if err = tx.Commit(); err != nil {
		return review.ModelReduction{}, err
	}
	return artifact, nil
}
