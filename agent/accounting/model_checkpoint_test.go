package accounting

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func TestPostgresModelCheckpointBindsEvidenceAndCursorBeforeReservation(t *testing.T) {
	for _, fault := range []string{"", "attempt", "execution", "policy", "SCM", "cursor", "role"} {
		t.Run(fault, func(t *testing.T) {
			a, i, _ := preparedActionFixture(t)
			ctx := context.Background()
			agg, err := a.LoadAggregate(ctx, *i.Job)
			if err != nil {
				t.Fatal(err)
			}
			i.Step = "step5"
			i.Invocation = "complete"
			rc := &review.Context{Source: agg.Source}
			rc.AddFindings("prior response")
			cpctx, err := review.WithModelCheckpoint(ctx, rc, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 1})
			if err != nil {
				t.Fatal(err)
			}
			i.ModelCheckpoint = review.ModelCheckpointFromContext(cpctx)
			cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "attempt":
				cp.Source.Attempt.AttemptID = "different"
			case "execution":
				cp.Source.Execution.DeadlineAt = cp.Source.Execution.DeadlineAt.Add(time.Minute)
			case "policy":
				cp.Source.Policy.Digest = "different"
			case "SCM":
				cp.Source.SCM.Files[0].Patch = "+different"
			case "cursor":
				cp.Cursor.BatchIndex = 1
			case "role":
				i.Role = "formatter"
			}
			i.ModelCheckpoint, _ = json.Marshal(cp)
			p, err := a.PrepareAction(ctx, i)
			if fault != "" {
				if err == nil {
					t.Fatal("checkpoint contradiction reserved spending")
				}
				var actions, holds int
				_ = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions)
				_ = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
				if actions != 0 || holds != 0 {
					t.Fatal("invalid checkpoint leaked holds")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			mutated := i
			changed := cp
			changed.RawResponses = append(append([]string(nil), cp.RawResponses...), "forged prior response")
			mutated.ModelCheckpoint, _ = json.Marshal(changed)
			if _, err = a.PrepareAction(ctx, mutated); err == nil || !strings.Contains(err.Error(), "conflicts with immutable request") {
				t.Fatalf("existing action rebound checkpoint or failed for wrong reason: %v", err)
			}
			// Detached restart recovery returns the same evidence, raw context and slot.
			reopened := ledgerAuthority(t, a.db, ledgerLimits())
			claim, err := reopened.ReclaimPrepared(ctx, p, "checkpoint-recovery", time.Minute)
			if err != nil || claim.Intent == nil || !bytes.Equal(claim.Intent.ModelCheckpoint, i.ModelCheckpoint) {
				t.Fatalf("checkpoint lost at reclaim: %#v %v", claim, err)
			}
		})
	}
}
