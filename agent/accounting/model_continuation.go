package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func reductionKey(runID, group string) string {
	return "reduction-" + strings.TrimPrefix(hash([]byte(runID+"\x00"+group)), "sha256:")
}
func continuationKey(parent string) string {
	return "model-child-" + strings.TrimPrefix(hash([]byte(parent+"\x00v1")), "sha256:")
}
func sameModelJSON(a, b any) bool {
	x, e := json.Marshal(a)
	y, f := json.Marshal(b)
	return e == nil && f == nil && bytes.Equal(x, y)
}

// AdvanceModelContinuation is a trusted-host operation. A checked parent becomes
// exactly one prepared child, all holds and a confined job epoch in one commit.
func (a *Authority) AdvanceModelContinuation(ctx context.Context, parentActionID string, builder orchestrator.ModelContinuationBuilder) (orchestrator.PreparedAction, error) {
	var empty orchestrator.PreparedAction
	if !validID(parentActionID) || builder == nil {
		return empty, errors.New("parent action and qualified builder required")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	parent, err := a.readModelReductionTx(ctx, tx, parentActionID)
	if err != nil {
		return empty, err
	}
	parentID := reductionKey(parent.Source.Run.ID, parent.GroupDigest)
	if receipt, err := a.continuationReceiptTx(ctx, tx, parentID); err != nil {
		return empty, err
	} else if receipt != nil {
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return *receipt, nil
	}
	var token string
	var grant sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.claim_token,j.recovery_action_id FROM budget_inbox i JOIN budget_jobs j USING(run_id) WHERE i.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND i.state='running' AND i.deadline>clock_timestamp() AND j.phase='effect_started' AND j.dispatch_allowed AND j.attempt_id=$4 FOR UPDATE OF i,j`, parent.Source.Run.ID, a.cfg.BoundaryID, a.cfg.ProjectID, parent.Source.Attempt.AttemptID).Scan(&token, &grant)
	if err != nil {
		return empty, errors.New("current continuation job unavailable")
	}
	if !validID(token) {
		return empty, errors.New("continuation owner token is invalid")
	}
	if receipt, err := a.continuationReceiptTx(ctx, tx, parentID); err != nil {
		return empty, err
	} else if receipt != nil {
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return *receipt, nil
	}
	parent, err = a.readModelReductionTx(ctx, tx, parentActionID)
	if err != nil {
		return empty, err
	}
	var parentDigest string
	if err = tx.QueryRowContext(ctx, `SELECT artifact_digest FROM budget_model_reductions WHERE id=$1 AND run_id=$2 AND attempt_id=$3 FOR SHARE`, parentID, parent.Source.Run.ID, parent.Source.Attempt.AttemptID).Scan(&parentDigest); err != nil {
		return empty, err
	}
	if err = a.checkReductionProofTx(ctx, tx, parentID, parent); err != nil {
		return empty, err
	}
	var epochMatches bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x JOIN budget_action_leases l ON l.action_id=x.action_id WHERE x.reduction_id=$1 AND l.job_token=$2 AND l.sending_fence=l.fence AND l.sending_fence IS NOT NULL)`, parentID, token).Scan(&epochMatches); err != nil {
		return empty, err
	}
	if !epochMatches {
		return empty, errors.New("job epoch no longer belongs to the parent result")
	}
	allowed := !grant.Valid
	for _, input := range parent.Inputs {
		if grant.String == input.ActionID {
			allowed = true
		}
	}
	if !allowed {
		return empty, errors.New("continuation cursor was already superseded")
	}
	var unresolved bool
	query := `SELECT EXISTS(SELECT 1 FROM budget_actions a WHERE a.attempt_id=$1 AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=$2 AND (a.state<>'result_recorded' OR NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x WHERE x.action_id=a.id)))`
	if !grant.Valid {
		query = `SELECT EXISTS(SELECT 1 FROM budget_actions a WHERE a.attempt_id=$1 AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=$2 AND NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x WHERE x.action_id=a.id AND x.reduction_id=$3))`
	}
	args := []any{parent.Source.Attempt.AttemptID, parent.Source.Run.ID}
	if !grant.Valid {
		args = append(args, parentID)
	}
	if err = tx.QueryRowContext(ctx, query, args...).Scan(&unresolved); err != nil {
		return empty, err
	}
	if unresolved {
		return empty, errors.New("unreduced or unresolved sibling effects block continuation")
	}
	plan, err := invokeContinuationBuilder(builder, parent)
	if err != nil {
		return empty, err
	}
	if err = validateContinuationPlan(plan, parent); err != nil {
		return empty, err
	}
	childID := continuationKey(parentID)
	newToken, err := review.NewOpaqueID("continued-job-")
	if err != nil {
		return empty, err
	}
	i := plan.Intent
	i.ActionID = childID
	i.Job = &review.JobClaim{RunID: parent.Source.Run.ID, Token: newToken}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET claim_token=$2,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID, newToken); err != nil {
		return empty, err
	}
	// The deferred FK permits the exact child pointer before insertion, but a
	// dangling pointer cannot commit. No unrestricted job grant is introduced.
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET recovery_action_id=$2,updated_at=clock_timestamp() WHERE run_id=$1`, i.Job.RunID, childID); err != nil {
		return empty, err
	}
	p, err := a.reserveActionTx(ctx, tx, i, true)
	if err != nil {
		return empty, err
	}
	if p.AlreadyStarted {
		return empty, errors.New("continuation child already sent without its parent link")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_model_continuations(parent_reduction_id,parent_digest,child_action_id,builder_version) VALUES($1,$2,$3,1)`, parentID, parentDigest, childID); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return orchestrator.PreparedAction{ID: p.ID, RequestDigest: p.RequestDigest, Fence: p.Fence}, nil
}

func (a *Authority) continuationReceiptTx(ctx context.Context, tx *sql.Tx, parentID string) (*orchestrator.PreparedAction, error) {
	var id, storedDigest, currentDigest, state string
	var fence int64
	var fenced bool
	err := tx.QueryRowContext(ctx, `SELECT c.child_action_id,c.parent_digest,m.artifact_digest,a.state,l.fence,a.fenced FROM budget_model_continuations c JOIN budget_model_reductions m ON m.id=c.parent_reduction_id JOIN budget_actions a ON a.id=c.child_action_id JOIN budget_action_leases l ON l.action_id=a.id WHERE c.parent_reduction_id=$1`, parentID).Scan(&id, &storedDigest, &currentDigest, &state, &fence, &fenced)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if storedDigest != currentDigest || id != continuationKey(parentID) || !fenced {
		return nil, errors.New("continuation link integrity failure")
	}
	i, _, err := a.storedAction(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return &orchestrator.PreparedAction{ID: id, RequestDigest: i.RequestDigest, Fence: uint64(fence), AlreadyStarted: state != "prepared"}, nil
}

func (a *Authority) checkReductionProofTx(ctx context.Context, tx *sql.Tx, parentID string, parent review.ModelReduction) error {
	if len(parent.Inputs) == 0 || len(parent.Inputs) > 128 {
		return errors.New("invalid parent input bound")
	}
	var refs []review.ModelResultRef
	var rawResponses []string
	var checkpoint review.ModelCheckpoint
	for index, ref := range parent.Inputs {
		i, _, err := a.storedAction(ctx, tx, ref.ActionID)
		if err != nil {
			return err
		}
		cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
		if err != nil {
			return err
		}
		if index == 0 {
			checkpoint = cp
			rawResponses = append([]string(nil), cp.RawResponses...)
		}
		key, err := review.ModelCheckpointGroupDigest(i.ModelCheckpoint)
		if err != nil {
			return err
		}
		if key != parent.GroupDigest || i.Job == nil || i.Job.RunID != parent.Source.Run.ID || cp.Cursor.BatchIndex != index {
			return errors.New("parent reduction input group changed")
		}
		var recorded, original []byte
		var responseDigest string
		err = tx.QueryRowContext(ctx, `SELECT r.outcome,a.outcome,r.response_digest FROM budget_action_results r JOIN budget_actions a ON a.id=r.action_id JOIN budget_model_reduction_inputs x ON x.action_id=a.id WHERE a.id=$1 AND x.reduction_id=$2 AND a.state='result_recorded' AND r.run_id=$3`, i.ActionID, parentID, parent.Source.Run.ID).Scan(&recorded, &original, &responseDigest)
		if err != nil {
			return err
		}
		if !bytes.Equal(recorded, original) {
			return errors.New("parent journal outcome changed")
		}
		var out orchestrator.DispatchOutcome
		if err = json.Unmarshal(recorded, &out); err != nil {
			return err
		}
		if !out.ResponseReceived || hash(out.Response) != responseDigest || out.ResponseDigest != responseDigest {
			return errors.New("parent response integrity failure")
		}
		refs = append(refs, review.ModelResultRef{ActionID: i.ActionID, RequestDigest: i.RequestDigest, ResponseDigest: responseDigest, CheckpointDigest: hash(i.ModelCheckpoint)})
		rawResponses = append(rawResponses, string(out.Response))
	}
	want := 1
	if checkpoint.Cursor.Phase == review.ModelReviewBatches {
		want = checkpoint.Cursor.BatchCount
	}
	if len(refs) != want {
		return errors.New("parent reduction is missing batch inputs")
	}
	return validateModelReduction(parent, refs, rawResponses, parent.GroupDigest, checkpoint)
}

func invokeContinuationBuilder(builder orchestrator.ModelContinuationBuilder, parent review.ModelReduction) (plan orchestrator.ModelContinuationPlan, err error) {
	defer func() {
		if recover() != nil {
			plan = orchestrator.ModelContinuationPlan{}
			err = errors.New("continuation builder panicked")
		}
	}()
	raw, err := json.Marshal(parent)
	if err != nil {
		return plan, err
	}
	var detached review.ModelReduction
	if err = json.Unmarshal(raw, &detached); err != nil {
		return plan, err
	}
	return builder.BuildModelContinuation(detached)
}

func validateContinuationPlan(plan orchestrator.ModelContinuationPlan, parent review.ModelReduction) error {
	if plan.Version != 1 || plan.ParentGroupDigest != parent.GroupDigest || plan.ParentInputDigest != parent.InputDigest || parent.NextCursor == nil || parent.Routing == nil || plan.Intent.ActionID != "" || plan.Intent.Job != nil || plan.Intent.Invocation != "complete" || plan.Intent.Role != "formatter" {
		return errors.New("invalid confined continuation plan")
	}
	want := review.ModelRepairFindings
	if parent.State == review.ModelReductionNeedsCoverageRepair {
		want = review.ModelRepairCoverage
	} else if parent.State != review.ModelReductionNeedsFindingsRepair {
		return errors.New("parent does not authorize formatter continuation")
	}
	i := plan.Intent
	cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
	if err != nil {
		return err
	}
	if cp.Cursor.Phase != want || !sameModelJSON(cp.Cursor, *parent.NextCursor) || !sameModelJSON(cp.Source, parent.Source) || !sameModelJSON(cp.RawResponses, parent.RawResponses) || !sameModelJSON(cp.Routing, parent.Routing) || cp.Validation == nil || !sameModelJSON(*cp.Validation, parent.Validation) {
		return errors.New("continuation changed frozen parent context")
	}
	chain, ok := parent.Routing.Roles["formatter"]
	if !ok || len(chain.Routes) == 0 {
		return errors.New("parent formatter route unavailable")
	}
	var req orchestrator.LLMRequest
	if err = json.Unmarshal(i.Payload, &req); err != nil {
		return err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, i.Payload) || hash(raw) != i.RequestDigest || len(req.Tools) != 0 || i.Provider != chain.Routes[0].Provider || i.Model != chain.Routes[0].Model || req.Model != i.Model || req.MaxTokens != chain.MaxTokens {
		return errors.New("continuation changed original route or request bound")
	}
	return orchestrator.ValidateModelCheckpoint(i)
}

func (a *Authority) confinedContinuationTx(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) (bool, error) {
	if i.Job == nil {
		return false, nil
	}
	var parentID, parentDigest, currentDigest, parentAction string
	err := tx.QueryRowContext(ctx, `SELECT c.parent_reduction_id,c.parent_digest,m.artifact_digest,(SELECT min(x.action_id) FROM budget_model_reduction_inputs x WHERE x.reduction_id=m.id) FROM budget_model_continuations c JOIN budget_model_reductions m ON m.id=c.parent_reduction_id JOIN budget_jobs j ON j.run_id=m.run_id WHERE c.child_action_id=$1 AND m.run_id=$2 AND m.attempt_id=$3 AND j.recovery_action_id=$1 AND j.phase='effect_started'`, i.ActionID, i.Job.RunID, i.Attempt.AttemptID).Scan(&parentID, &parentDigest, &currentDigest, &parentAction)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if parentDigest != currentDigest || i.ActionID != continuationKey(parentID) {
		return false, errors.New("prepared continuation parent link changed")
	}
	parent, err := a.readModelReductionTx(ctx, tx, parentAction)
	if err != nil {
		return false, err
	}
	if err = a.checkReductionProofTx(ctx, tx, parentID, parent); err != nil {
		return false, err
	}
	proposal := i
	proposal.ActionID = ""
	proposal.Job = nil
	if err = validateContinuationPlan(orchestrator.ModelContinuationPlan{Version: 1, ParentGroupDigest: parent.GroupDigest, ParentInputDigest: parent.InputDigest, Intent: proposal}, parent); err != nil {
		return false, err
	}
	var unresolved bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_actions a WHERE a.attempt_id=$1 AND a.id<>$2 AND convert_from(a.request,'UTF8')::jsonb #>> '{Job,RunID}'=$3 AND (a.state<>'result_recorded' OR NOT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x WHERE x.action_id=a.id)))`, i.Attempt.AttemptID, i.ActionID, i.Job.RunID).Scan(&unresolved)
	if err != nil {
		return false, err
	}
	if unresolved {
		return false, errors.New("continuation has unresolved sibling effects")
	}
	return true, nil
}

func (a *Authority) AdvancePendingModelContinuations(ctx context.Context, limit int, builder orchestrator.ModelContinuationBuilder) (int, error) {
	if limit < 1 || limit > 128 || builder == nil {
		return 0, errors.New("invalid continuation batch or builder")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT min(x.action_id) FROM budget_model_reductions m JOIN budget_model_reduction_inputs x ON x.reduction_id=m.id JOIN budget_jobs j ON j.run_id=m.run_id JOIN budget_inbox i ON i.run_id=j.run_id WHERE i.boundary_id=$1 AND i.project_id=$2 AND i.state='running' AND i.deadline>clock_timestamp() AND j.phase='effect_started' AND j.dispatch_allowed AND m.state IN ('needs_findings_repair','needs_coverage_repair') AND convert_from(m.artifact,'UTF8')::jsonb #> '{Routing,Roles,formatter}' IS NOT NULL AND NOT EXISTS(SELECT 1 FROM budget_model_continuations c WHERE c.parent_reduction_id=m.id) GROUP BY m.id,m.created_at ORDER BY m.created_at,m.id LIMIT $3`, a.cfg.BoundaryID, a.cfg.ProjectID, limit)
	if err != nil {
		return 0, err
	}
	var actions []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		actions = append(actions, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	count := 0
	for _, id := range actions {
		if _, err = a.AdvanceModelContinuation(ctx, id, builder); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
