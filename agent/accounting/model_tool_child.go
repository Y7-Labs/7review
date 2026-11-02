package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func checkToolChildStorage(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT parent_reduction_id,parent_digest,observation_id,observation_digest,child_action_id,builder_version FROM budget_model_tool_children LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

// AdvanceModelToolContinuation consumes a committed observation epoch, not the
// model parent's revoked epoch. Child, reservation, proof and new cursor commit
// together. Returning a historical preparation receipt never grants sending.
func (a *Authority) AdvanceModelToolContinuation(ctx context.Context, actionID string, builder orchestrator.ModelToolContinuationBuilder) (orchestrator.PreparedAction, error) {
	var empty orchestrator.PreparedAction
	if !validID(actionID) || builder == nil {
		return empty, errors.New("qualified tool parent and builder required")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	parent, err := a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return empty, err
	}
	parentID := reductionKey(parent.Source.Run.ID, parent.GroupDigest)
	if receipt, err := a.toolChildReceiptTx(ctx, tx, parentID); err != nil {
		return empty, err
	} else if receipt != nil {
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return *receipt, nil
	}
	var token, cursor, observationID string
	err = tx.QueryRowContext(ctx, `SELECT i.claim_token,j.recovery_action_id,j.tool_observation_id FROM budget_inbox i JOIN budget_jobs j USING(run_id) WHERE i.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND i.state='running' AND i.deadline>clock_timestamp() AND j.phase='effect_started' AND j.dispatch_allowed AND j.attempt_id=$4 FOR UPDATE OF i,j`, parent.Source.Run.ID, a.cfg.BoundaryID, a.cfg.ProjectID, parent.Source.Attempt.AttemptID).Scan(&token, &cursor, &observationID)
	if err != nil {
		return empty, errors.New("current tool continuation epoch unavailable")
	}
	if receipt, err := a.toolChildReceiptTx(ctx, tx, parentID); err != nil {
		return empty, err
	} else if receipt != nil {
		if err = tx.Commit(); err != nil {
			return empty, err
		}
		return *receipt, nil
	}
	parent, err = a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return empty, err
	}
	record, err := a.readToolEvidenceTx(ctx, tx, parentID, parent)
	if err != nil {
		return empty, err
	}
	if observationID != toolObservationKey(parentID) || token != record.OwnerToken || cursor != record.ParentActionID {
		return empty, errors.New("tool observation ownership was superseded")
	}
	if err = a.checkTerminalLineageTx(ctx, tx, parentID, parent); err != nil {
		return empty, err
	}
	i, _, err := a.storedAction(ctx, tx, record.ParentActionID)
	if err != nil {
		return empty, err
	}
	binding, err := a.binding(i)
	if err != nil {
		return empty, err
	}
	if err = a.lockAttemptAuthority(ctx, tx, i, binding); err != nil {
		return empty, err
	}
	plan, err := invokeToolContinuationBuilder(builder, parent, record.Artifact)
	if err != nil {
		return empty, err
	}
	if err = validateToolContinuationPlan(plan, parent, record.Artifact); err != nil {
		return empty, err
	}
	newToken, err := review.NewOpaqueID("tool-child-job-")
	if err != nil {
		return empty, err
	}
	childID := continuationKey(parentID)
	child := plan.Intent
	child.ActionID, child.Job = childID, &review.JobClaim{RunID: parent.Source.Run.ID, Token: newToken}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET claim_token=$2,updated_at=clock_timestamp() WHERE run_id=$1`, child.Job.RunID, newToken); err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET recovery_action_id=$2,updated_at=clock_timestamp() WHERE run_id=$1`, child.Job.RunID, childID); err != nil {
		return empty, err
	}
	prepared, err := a.reserveActionTx(ctx, tx, child, true)
	if err != nil {
		return empty, err
	}
	if prepared.AlreadyStarted {
		return empty, errors.New("tool child already sent without its proof")
	}
	var observationDigest string
	if err = tx.QueryRowContext(ctx, `SELECT receipt_digest FROM budget_model_tool_observations WHERE id=$1 FOR SHARE`, observationID).Scan(&observationDigest); err != nil {
		return empty, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_model_tool_children(parent_reduction_id,parent_digest,observation_id,observation_digest,child_action_id,builder_version) VALUES($1,$2,$3,$4,$5,1)`, parentID, record.ParentDigest, observationID, observationDigest, childID); err != nil {
		return empty, err
	}
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	return orchestrator.PreparedAction{ID: prepared.ID, RequestDigest: prepared.RequestDigest, Fence: prepared.Fence}, nil
}

func invokeToolContinuationBuilder(builder orchestrator.ModelToolContinuationBuilder, parent review.ModelReduction, observations review.ModelToolObservations) (plan orchestrator.ModelContinuationPlan, err error) {
	defer func() {
		if recover() != nil {
			plan = orchestrator.ModelContinuationPlan{}
			err = errors.New("tool continuation builder panicked")
		}
	}()
	raw, err := json.Marshal(struct {
		Parent       review.ModelReduction
		Observations review.ModelToolObservations
	}{parent, observations})
	if err != nil {
		return plan, err
	}
	var frozen struct {
		Parent       review.ModelReduction
		Observations review.ModelToolObservations
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err = d.Decode(&frozen); err != nil {
		return plan, err
	}
	return builder.BuildModelToolContinuation(frozen.Parent, frozen.Observations)
}

func validateToolContinuationPlan(plan orchestrator.ModelContinuationPlan, parent review.ModelReduction, observations review.ModelToolObservations) error {
	if _, err := observations.SnapshotUsage(parent); err != nil {
		return err
	}
	if plan.Version != 1 || plan.ParentGroupDigest != parent.GroupDigest || plan.ParentInputDigest != parent.InputDigest || parent.Routing == nil || parent.Source.Report.Draft != "" || parent.Source.Report.Final != "" || !parent.Source.Policy.AllowsCapability("model.review") || plan.Intent.ActionID != "" || plan.Intent.Job != nil || plan.Intent.Invocation != "complete" || plan.Intent.Role != "reasoner" || plan.Intent.Step != fmt.Sprintf("tool_augmented_review_round_%d", parent.NextCursor.Round) {
		return errors.New("invalid tool continuation proposal")
	}
	i := plan.Intent
	cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
	if err != nil {
		return err
	}
	source := parent.Source.Clone()
	source.ToolRequests = append(source.ToolRequests, observations.Requests...)
	source.ToolObservations = append(source.ToolObservations, observations.Observations...)
	if !sameModelJSON(cp.Source, source) || !sameModelJSON(cp.Cursor, *parent.NextCursor) || !sameModelJSON(cp.RawResponses, parent.RawResponses) || !sameModelJSON(cp.Routing, parent.Routing) || cp.Validation == nil || !sameModelJSON(*cp.Validation, parent.Validation) {
		return errors.New("tool child changed frozen evidence or validation")
	}
	chain, ok := parent.Routing.Roles["reasoner"]
	if !ok || len(chain.Routes) == 0 {
		return errors.New("frozen reasoner route unavailable")
	}
	var req orchestrator.LLMRequest
	if err = json.Unmarshal(i.Payload, &req); err != nil {
		return err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, i.Payload) || hash(raw) != i.RequestDigest || i.Provider != chain.Routes[0].Provider || i.Model != chain.Routes[0].Model || req.Model != i.Model || req.MaxTokens != chain.MaxTokens || !sameModelJSON(req.Tools, orchestrator.SnapshotToolDefinitions(source)) {
		return errors.New("tool child changed route, bound or native tool scope")
	}
	return orchestrator.ValidateModelCheckpoint(i)
}

func (a *Authority) toolChildReceiptTx(ctx context.Context, tx *sql.Tx, parentID string) (*orchestrator.PreparedAction, error) {
	var childID string
	err := tx.QueryRowContext(ctx, `SELECT child_action_id FROM budget_model_tool_children WHERE parent_reduction_id=$1`, parentID).Scan(&childID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	i, _, err := a.storedAction(ctx, tx, childID)
	if err != nil {
		return nil, err
	}
	linked, parent, found, err := a.readContinuationParentTx(ctx, tx, i)
	if err != nil {
		return nil, err
	}
	if !found || linked != parentID {
		return nil, errors.New("tool child receipt lost its parent")
	}
	if err = a.checkModelLineageTx(ctx, tx, parentID, parent, false); err != nil {
		return nil, err
	}
	var state string
	var fence int64
	var fenced bool
	if err = tx.QueryRowContext(ctx, `SELECT a.state,l.fence,a.fenced FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.id=$1`, childID).Scan(&state, &fence, &fenced); err != nil {
		return nil, err
	}
	if !fenced || fence < 0 {
		return nil, errors.New("tool child receipt is not fenced")
	}
	return &orchestrator.PreparedAction{ID: childID, RequestDigest: i.RequestDigest, Fence: uint64(fence), AlreadyStarted: state != "prepared"}, nil
}

// Both edge kinds use the same action identity, but never infer a missing link.
// Tool evidence verification here does not recursively walk ancestry: the
// bounded caller's lineage walk verifies each parent once.
func (a *Authority) readContinuationParentTx(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) (string, review.ModelReduction, bool, error) {
	var empty review.ModelReduction
	rows, err := tx.QueryContext(ctx, `SELECT c.parent_reduction_id,c.parent_digest,c.builder_version,NULL::text,NULL::text FROM budget_model_continuations c WHERE c.child_action_id=$1 UNION ALL SELECT c.parent_reduction_id,c.parent_digest,c.builder_version,c.observation_id,c.observation_digest FROM budget_model_tool_children c WHERE c.child_action_id=$1`, i.ActionID)
	if err != nil {
		return "", empty, false, err
	}
	var parentID, digest string
	var version, count int
	var observationID, observationDigest sql.NullString
	for rows.Next() {
		count++
		if err = rows.Scan(&parentID, &digest, &version, &observationID, &observationDigest); err != nil {
			rows.Close()
			return "", empty, false, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", empty, false, err
	}
	if count == 0 {
		return "", empty, false, nil
	}
	if count != 1 || version != 1 || i.Job == nil || i.ActionID != continuationKey(parentID) {
		return "", empty, false, errors.New("ambiguous or changed continuation edge")
	}
	var parentAction, currentDigest, runID, attemptID string
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT min(action_id) FROM budget_model_reduction_inputs WHERE reduction_id=m.id),m.artifact_digest,m.run_id,m.attempt_id FROM budget_model_reductions m WHERE m.id=$1`, parentID).Scan(&parentAction, &currentDigest, &runID, &attemptID); err != nil {
		return "", empty, false, err
	}
	if digest != currentDigest || runID != i.Job.RunID || attemptID != i.Attempt.AttemptID {
		return "", empty, false, errors.New("continuation edge attribution changed")
	}
	parent, err := a.readModelReductionTx(ctx, tx, parentAction)
	if err != nil {
		return "", empty, false, err
	}
	proposal := i
	proposal.ActionID = ""
	proposal.Job = nil
	plan := orchestrator.ModelContinuationPlan{Version: version, ParentGroupDigest: parent.GroupDigest, ParentInputDigest: parent.InputDigest, Intent: proposal}
	if observationID.Valid {
		record, err := a.readToolEvidenceTx(ctx, tx, parentID, parent)
		if err != nil {
			return "", empty, false, err
		}
		var storedDigest string
		if err = tx.QueryRowContext(ctx, `SELECT receipt_digest FROM budget_model_tool_observations WHERE id=$1`, observationID.String).Scan(&storedDigest); err != nil {
			return "", empty, false, err
		}
		if observationID.String != toolObservationKey(parentID) || !observationDigest.Valid || storedDigest != observationDigest.String {
			return "", empty, false, errors.New("tool child observation proof changed")
		}
		if err = validateToolContinuationPlan(plan, parent, record.Artifact); err != nil {
			return "", empty, false, err
		}
	} else if err = validateContinuationPlan(plan, parent); err != nil {
		return "", empty, false, err
	}
	return parentID, parent, true, nil
}

func (a *Authority) confinedToolChildTx(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) (bool, error) {
	parentID, parent, found, err := a.readContinuationParentTx(ctx, tx, i)
	if err != nil || !found {
		return false, err
	}
	var current bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_model_tool_children c JOIN budget_jobs j ON j.run_id=$2 WHERE c.child_action_id=$1 AND j.recovery_action_id=$1 AND j.tool_observation_id=c.observation_id AND j.phase='effect_started')`, i.ActionID, i.Job.RunID).Scan(&current); err != nil {
		return false, err
	}
	if !current {
		return false, errors.New("tool child is not the current observed cursor")
	}
	if err = a.checkModelLineageTx(ctx, tx, parentID, parent, true, i.ActionID); err != nil {
		return false, err
	}
	return true, nil
}
