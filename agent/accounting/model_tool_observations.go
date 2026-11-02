package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

var ErrToolObservationsNotFound = errors.New("no durable snapshot tool observations")

type toolObservationScope struct{ ID, Kind, Authority, Origin string }
type toolObservationRecord struct {
	Version                                int
	ParentID, ParentDigest, ParentActionID string
	ParentToken, OwnerToken, Currency      string
	AccountedAt                            time.Time
	Amount                                 review.ResourceLimitScope
	Scopes                                 []toolObservationScope
	Artifact                               review.ModelToolObservations
}

func toolObservationKey(parent string) string {
	return "snapshot-tools-" + strings.TrimPrefix(hash([]byte(parent+"\x00v1")), "sha256:")
}

func checkToolObservationStorage(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT j.tool_observation_id,o.parent_reduction_id,o.run_id,o.attempt_id,o.receipt,o.receipt_digest,s.scope_id,s.origin_period FROM budget_jobs j LEFT JOIN budget_model_tool_observations o ON o.id=j.tool_observation_id LEFT JOIN budget_tool_observation_scopes s ON s.observation_id=o.id LIMIT 0`)
	if err != nil {
		return err
	}
	return rows.Close()
}

// CommitModelToolObservations accepts a trusted snapshot-only renderer, not an
// external tool executor. Pure rendering may repeat after rollback; acceptance,
// exact local charges and old-frame revocation happen together or not at all.
func (a *Authority) CommitModelToolObservations(ctx context.Context, actionID string, observer orchestrator.ModelToolObserver) (*review.ModelToolObservations, error) {
	if !validID(actionID) || observer == nil {
		return nil, errors.New("parent action and snapshot renderer required")
	}
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	parent, err := a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return nil, err
	}
	parentID := reductionKey(parent.Source.Run.ID, parent.GroupDigest)
	if result, err := a.readToolObservationsTx(ctx, tx, parentID, parent); !errors.Is(err, ErrToolObservationsNotFound) {
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	if parent.State != review.ModelReductionNeedsTools {
		return nil, errors.New("parent does not request snapshot tools")
	}
	var token string
	var grant, previous sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT i.claim_token,j.recovery_action_id,j.tool_observation_id FROM budget_inbox i JOIN budget_jobs j USING(run_id) WHERE i.run_id=$1 AND i.boundary_id=$2 AND i.project_id=$3 AND i.state='running' AND i.deadline>clock_timestamp() AND j.phase='effect_started' AND j.dispatch_allowed AND j.attempt_id=$4 FOR UPDATE OF i,j`, parent.Source.Run.ID, a.cfg.BoundaryID, a.cfg.ProjectID, parent.Source.Attempt.AttemptID).Scan(&token, &grant, &previous)
	if err != nil {
		return nil, errors.New("current snapshot observation job unavailable")
	}
	// A competing committer may have completed while this transaction waited.
	if result, err := a.readToolObservationsTx(ctx, tx, parentID, parent); !errors.Is(err, ErrToolObservationsNotFound) {
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	parent, err = a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return nil, err
	}
	var parentDigest string
	if err = tx.QueryRowContext(ctx, `SELECT artifact_digest FROM budget_model_reductions WHERE id=$1 AND run_id=$2 AND attempt_id=$3 FOR SHARE`, parentID, parent.Source.Run.ID, parent.Source.Attempt.AttemptID).Scan(&parentDigest); err != nil {
		return nil, err
	}
	if err = a.checkTerminalLineageTx(ctx, tx, parentID, parent); err != nil {
		return nil, err
	}
	if previous.Valid {
		var linked bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_model_tool_children c JOIN budget_model_reduction_inputs x ON x.action_id=c.child_action_id WHERE x.reduction_id=$1 AND c.observation_id=$2 AND x.action_id=$3)`, parentID, previous.String, grant).Scan(&linked); err != nil {
			return nil, err
		}
		if !linked {
			return nil, errors.New("snapshot observation cursor has no checked predecessor")
		}
	}
	if !grant.Valid {
		origin, err := a.batchOriginEpochTx(ctx, tx, parent)
		if err != nil {
			return nil, err
		}
		if origin.Valid && token != origin.String {
			return nil, errors.New("recovered snapshot batch lost its confined action cursor")
		}
	}
	representative := parent.Inputs[0].ActionID
	if grant.Valid {
		representative = grant.String
	}
	if err = a.checkToolParentEpochTx(ctx, tx, parentID, parent, token, representative); err != nil {
		return nil, err
	}
	i, _, err := a.storedAction(ctx, tx, representative)
	if err != nil {
		return nil, err
	}
	i.Job = &review.JobClaim{RunID: parent.Source.Run.ID, Token: token}
	binding, err := a.binding(i)
	if err != nil {
		return nil, err
	}
	refs := a.scopeRefs(i, binding)
	if err = checkToolParentHoldsTx(ctx, tx, parent, refs); err != nil {
		return nil, err
	}
	if err = a.lockPreparedAuthority(ctx, tx, i); err != nil {
		return nil, err
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	for n := range refs {
		if refs[n].kind != "attempt" {
			refs[n].origin, err = periodID(refs[n].limit.Period, now)
			if err != nil {
				return nil, err
			}
		}
		var authority string
		if err = tx.QueryRowContext(ctx, `SELECT authority_digest FROM budget_scopes WHERE id=$1`, refs[n].id).Scan(&authority); err != nil {
			return nil, err
		}
		if authority != refs[n].authority {
			return nil, errors.New("snapshot scope authority changed")
		}
		if err = checkCapacity(ctx, tx, refs[n], review.ResourceLimitScope{ToolCalls: int64(len(parent.ToolRequests))}); err != nil {
			return nil, err
		}
	}
	artifact, err := invokeToolObserver(ctx, observer, parent)
	if err != nil {
		return nil, err
	}
	amount, err := artifact.SnapshotUsage(parent)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Use database time after all waits/rendering, never a pre-lock period or an
	// observed model/provider duration. A boundary crossing cannot reset a hold.
	if err = tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if !now.Before(i.Execution.DeadlineAt) {
		return nil, errors.New("snapshot observation deadline expired")
	}
	if _, _, err = a.lockJob(ctx, tx, i.Job); err != nil {
		return nil, err
	}
	newToken, err := review.NewOpaqueID("observed-tools-")
	if err != nil {
		return nil, err
	}
	record := toolObservationRecord{Version: 1, ParentID: parentID, ParentDigest: parentDigest, ParentActionID: representative, ParentToken: token, OwnerToken: newToken, Currency: i.OperatorCeilings.Currency, AccountedAt: now, Amount: amount, Artifact: artifact}
	for n := range refs {
		ref := &refs[n]
		if ref.kind != "attempt" {
			ref.origin, err = periodID(ref.limit.Period, now)
			if err != nil {
				return nil, err
			}
		}
		if err = checkCapacity(ctx, tx, *ref, amount); err != nil {
			return nil, err
		}
		record.Scopes = append(record.Scopes, toolObservationScope{ref.id, ref.kind, ref.authority, ref.origin})
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	id := toolObservationKey(parentID)
	if _, err = tx.ExecContext(ctx, `INSERT INTO budget_model_tool_observations(id,parent_reduction_id,run_id,attempt_id,receipt,receipt_digest) VALUES($1,$2,$3,$4,$5,$6)`, id, parentID, artifact.RunID, artifact.AttemptID, raw, hash(raw)); err != nil {
		return nil, err
	}
	for _, ref := range record.Scopes {
		if _, err = tx.ExecContext(ctx, `INSERT INTO budget_tool_observation_scopes(observation_id,scope_id,origin_period) VALUES($1,$2,$3)`, id, ref.ID, ref.Origin); err != nil {
			return nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_inbox SET claim_token=$2,updated_at=clock_timestamp() WHERE run_id=$1`, artifact.RunID, newToken); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE budget_jobs SET recovery_action_id=$2,tool_observation_id=$3,updated_at=clock_timestamp() WHERE run_id=$1`, artifact.RunID, representative, id); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &artifact, nil
}

func checkToolParentHoldsTx(ctx context.Context, tx *sql.Tx, parent review.ModelReduction, refs []scopeRef) error {
	ids := []string{refs[0].id, refs[1].id, refs[2].id}
	for _, input := range parent.Inputs {
		var total, expected int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE scope_id=ANY($2::text[])) FROM budget_holds WHERE action_id=$1`, input.ActionID, ids).Scan(&total, &expected); err != nil {
			return err
		}
		if total != 3 || expected != 3 {
			return errors.New("snapshot parent has incomplete or foreign model holds")
		}
	}
	return nil
}

func invokeToolObserver(ctx context.Context, observer orchestrator.ModelToolObserver, parent review.ModelReduction) (result review.ModelToolObservations, err error) {
	defer func() {
		if recover() != nil {
			result = review.ModelToolObservations{}
			err = errors.New("snapshot tool renderer panicked")
		}
	}()
	raw, err := json.Marshal(parent)
	if err != nil {
		return result, err
	}
	var detached review.ModelReduction
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&detached); err != nil {
		return result, err
	}
	ctx, cancel := context.WithDeadline(ctx, parent.Source.Execution.DeadlineAt)
	defer cancel()
	return observer.ObserveModelTools(ctx, detached)
}

func (a *Authority) checkToolParentEpochTx(ctx context.Context, tx *sql.Tx, parentID string, parent review.ModelReduction, token, representative string) error {
	if !validID(token) {
		return errors.New("invalid snapshot parent epoch")
	}
	origin, err := a.batchOriginEpochTx(ctx, tx, parent)
	if err != nil {
		return err
	}
	var bound, changed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x JOIN budget_action_leases l ON l.action_id=x.action_id WHERE x.reduction_id=$1 AND x.action_id=$2 AND l.job_token=$3 AND l.sending_fence=l.fence AND l.fence>0)`, parentID, representative, token).Scan(&bound); err != nil {
		return err
	}
	if !bound {
		return errors.New("snapshot parent cursor has no current sending epoch")
	}
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM budget_model_reduction_inputs x LEFT JOIN budget_action_leases l ON l.action_id=x.action_id WHERE x.reduction_id=$1 AND (l.action_id IS NULL OR l.job_token IS NULL OR (l.job_token IS DISTINCT FROM $2 AND l.job_token IS DISTINCT FROM $3) OR l.sending_fence IS NULL OR l.sending_fence<>l.fence))`, parentID, token, origin).Scan(&changed); err != nil {
		return err
	}
	if changed {
		return errors.New("snapshot parent belongs to a stale job epoch")
	}
	return nil
}

func (a *Authority) LookupModelToolObservations(ctx context.Context, actionID string) (*review.ModelToolObservations, error) {
	tx, err := a.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	parent, err := a.readModelReductionTx(ctx, tx, actionID)
	if err != nil {
		return nil, err
	}
	result, err := a.readToolObservationsTx(ctx, tx, reductionKey(parent.Source.Run.ID, parent.GroupDigest), parent)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *Authority) readToolObservationsTx(ctx context.Context, tx *sql.Tx, parentID string, parent review.ModelReduction) (*review.ModelToolObservations, error) {
	record, err := a.readToolEvidenceTx(ctx, tx, parentID, parent)
	if err != nil {
		return nil, err
	}
	if err = a.checkModelLineageTx(ctx, tx, parentID, parent, false); err != nil {
		return nil, err
	}
	return &record.Artifact, nil
}

func (a *Authority) readToolEvidenceTx(ctx context.Context, tx *sql.Tx, parentID string, parent review.ModelReduction) (*toolObservationRecord, error) {
	record, err := readToolObservationRecordTx(ctx, tx, toolObservationKey(parentID))
	if err != nil {
		return nil, err
	}
	var parentDigest string
	if err = tx.QueryRowContext(ctx, `SELECT artifact_digest FROM budget_model_reductions WHERE id=$1`, parentID).Scan(&parentDigest); err != nil {
		return nil, err
	}
	if record.ParentID != parentID || record.ParentDigest != parentDigest {
		return nil, errors.New("snapshot observation parent evidence changed")
	}
	usage, err := record.Artifact.SnapshotUsage(parent)
	if err != nil {
		return nil, err
	}
	if !sameModelJSON(usage, record.Amount) {
		return nil, errors.New("snapshot observation charge changed")
	}
	if err = a.checkToolParentEpochTx(ctx, tx, parentID, parent, record.ParentToken, record.ParentActionID); err != nil {
		return nil, err
	}
	i, _, err := a.storedAction(ctx, tx, record.ParentActionID)
	if err != nil {
		return nil, err
	}
	// Historical proof uses frozen authority, not later operator configuration;
	// mutation above always uses current authority and current active ownership.
	historical := *a
	historical.cfg = a.cfg
	historical.cfg.Limits = i.OperatorCeilings.Clone()
	binding, err := historical.binding(i)
	if err != nil {
		return nil, err
	}
	if record.Currency != i.OperatorCeilings.Currency {
		return nil, errors.New("snapshot charge currency changed")
	}
	refs := historical.scopeRefs(i, binding)
	if err = checkToolParentHoldsTx(ctx, tx, parent, refs); err != nil {
		return nil, err
	}
	for n, ref := range refs {
		if ref.kind != "attempt" {
			ref.origin, err = periodID(ref.limit.Period, record.AccountedAt)
			if err != nil {
				return nil, err
			}
		}
		if record.Scopes[n] != (toolObservationScope{ref.id, ref.kind, ref.authority, ref.origin}) {
			return nil, errors.New("snapshot scope attribution changed")
		}
	}
	return record, nil
}

// This storage-level proof is also used by every capacity check. Keeping scope
// IDs in the hashed record lets a missing/moved link fail closed, not erase cost.
func readToolObservationRecordTx(ctx context.Context, tx *sql.Tx, id string) (*toolObservationRecord, error) {
	var raw []byte
	var digest, parentID, runID, attemptID string
	err := tx.QueryRowContext(ctx, `SELECT receipt,receipt_digest,parent_reduction_id,run_id,attempt_id FROM budget_model_tool_observations WHERE id=$1`, id).Scan(&raw, &digest, &parentID, &runID, &attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrToolObservationsNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 256<<10 || hash(raw) != digest {
		return nil, errors.New("snapshot observation receipt integrity failure")
	}
	var r toolObservationRecord
	if err = json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Version != 1 || r.ParentID != parentID || id != toolObservationKey(parentID) || r.Artifact.RunID != runID || r.Artifact.AttemptID != attemptID || !canonicalDigest(r.ParentDigest) || !validID(r.ParentActionID) || !validID(r.ParentToken) || !validID(r.OwnerToken) || r.ParentToken == r.OwnerToken || r.AccountedAt.IsZero() || len(r.Scopes) != 3 {
		return nil, errors.New("snapshot observation receipt attribution failure")
	}
	derived := review.ResourceLimitScope{ToolCalls: int64(len(r.Artifact.Requests))}
	if derived.ToolCalls < 1 || derived.ToolCalls > review.MaxModelToolRequests || len(r.Artifact.Observations) != len(r.Artifact.Requests) {
		return nil, errors.New("snapshot charge membership changed")
	}
	for _, o := range r.Artifact.Observations {
		raw, err := json.Marshal(o)
		if err != nil {
			return nil, err
		}
		derived.ObservationBytes += int64(len(raw))
	}
	if !sameModelJSON(derived, r.Amount) {
		return nil, errors.New("snapshot receipt amount is not derived from encoded observations")
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM budget_tool_observation_scopes WHERE observation_id=$1`, id).Scan(&count); err != nil {
		return nil, err
	}
	if count != 3 {
		return nil, errors.New("snapshot charge scope membership changed")
	}
	seen := map[string]bool{}
	for n, ref := range r.Scopes {
		if seen[ref.ID] || ref.Kind != []string{"attempt", "change", "project"}[n] || !canonicalDigest(ref.Authority) || ref.Origin == "" {
			return nil, errors.New("invalid snapshot scope receipt")
		}
		seen[ref.ID] = true
		var kind, authority, origin string
		if err = tx.QueryRowContext(ctx, `SELECT s.kind,s.authority_digest,l.origin_period FROM budget_tool_observation_scopes l JOIN budget_scopes s ON s.id=l.scope_id WHERE l.observation_id=$1 AND l.scope_id=$2`, id, ref.ID).Scan(&kind, &authority, &origin); err != nil {
			return nil, err
		}
		if kind != ref.Kind || authority != ref.Authority || origin != ref.Origin {
			return nil, errors.New("snapshot scope link integrity failure")
		}
	}
	return &r, nil
}

func snapshotChargesTx(ctx context.Context, tx *sql.Tx, ref scopeRef) ([]review.ResourceLimitScope, error) {
	rows, err := tx.QueryContext(ctx, `SELECT o.id FROM budget_model_tool_observations o WHERE (convert_from(o.receipt,'UTF8')::jsonb->'Scopes') @> jsonb_build_array(jsonb_build_object('ID',$1::text)) OR EXISTS(SELECT 1 FROM budget_tool_observation_scopes s WHERE s.observation_id=o.id AND s.scope_id=$1)`, ref.id)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var charges []review.ResourceLimitScope
	for _, id := range ids {
		r, err := readToolObservationRecordTx(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		found := false
		for _, scope := range r.Scopes {
			if scope.ID != ref.id {
				continue
			}
			found = true
			if scope.Authority != ref.authority || scope.Kind != ref.kind {
				return nil, errors.New("snapshot scope authority mismatch")
			}
			wantOrigin := "lifetime"
			if ref.kind != "attempt" {
				wantOrigin, err = periodID(ref.limit.Period, r.AccountedAt)
				if err != nil {
					return nil, err
				}
			}
			if scope.Origin != wantOrigin {
				return nil, errors.New("snapshot charge period differs from its accounting time")
			}
			if scope.Origin == ref.origin {
				charges = append(charges, r.Amount)
			}
		}
		if !found {
			return nil, errors.New("snapshot link is not present in its receipt")
		}
	}
	return charges, nil
}
