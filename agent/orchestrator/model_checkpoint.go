package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Y4NN777/7review/agent/review"
)

// ValidateModelCheckpoint checks host continuation metadata, not model output.
// Missing legacy checkpoints grant no continuation authority.
func ValidateModelCheckpoint(i DispatchIntent) error {
	if len(i.ModelCheckpoint) == 0 {
		return nil
	}
	cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
	if err != nil {
		return err
	}
	s := cp.Source
	equal := func(a, b any) bool {
		x, e := json.Marshal(a)
		y, f := json.Marshal(b)
		return e == nil && f == nil && bytes.Equal(x, y)
	}
	if s.Run.ID == "" || (i.Job != nil && s.Run.ID != i.Job.RunID) || !equal(s.Attempt, i.Attempt) || !equal(s.Execution, i.Execution) || !equal(s.Attestation, i.Attestation) || s.Policy.Digest != i.PolicyDigest || !equal(s.Policy.Limits, i.PolicyLimits) || s.Policy.OperatorCeilings == nil || !equal(*s.Policy.OperatorCeilings, i.OperatorCeilings) || !s.Policy.AllowsCapability("model.review") {
		return errors.New("model checkpoint changed action authority")
	}
	if s.Attestation == nil {
		return errors.New("model checkpoint missing attestation")
	}
	if _, err = review.NewVerifiedContext(s.Request, s.Attempt, s.Execution, *s.Attestation); err != nil {
		return err
	}
	wantRole, wantStep := RoleReasoner, "step5"
	switch cp.Cursor.Phase {
	case review.ModelReviewBatches:
		if cp.Cursor.BatchCount > 1 {
			wantStep = fmt.Sprintf("step5_batch%d", cp.Cursor.BatchIndex+1)
		}
	case review.ModelToolRound:
		wantStep = fmt.Sprintf("tool_augmented_review_round_%d", cp.Cursor.Round)
	case review.ModelRepairFindings:
		wantRole, wantStep = RoleFormatter, "repair_findings"
	case review.ModelRepairCoverage:
		wantRole, wantStep = RoleFormatter, "repair_skill_coverage"
	}
	if i.Role != string(wantRole) || i.Step != wantStep {
		return errors.New("model checkpoint cursor does not match dispatched stage")
	}
	return nil
}
