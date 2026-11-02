package accounting

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestUnusedReleaseRequiresCancellationAndPreservesAllScopeOrigins(t *testing.T) {
	a, i, actor, c, dsn := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.ClaimPrepared(ctx, p, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReleaseCancelledPrepared(ctx, p.ID); !errors.Is(err, ErrUnusedReleaseUnavailable) {
		t.Fatalf("active work refunded: %v", err)
	}
	cancelReceipt, err := a.CancelAttempt(ctx, actor, c)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.ReleaseCancelledPrepared(ctx, p.ID)
	if err != nil || r == nil || r.ActionID != p.ID || r.Fence != claim.Fence || len(r.Scopes) != 3 {
		t.Fatalf("release: %+v %v", r, err)
	}
	var holds, proofs, links int
	var state string
	var settled bool
	if err = a.db.QueryRow(`SELECT state,settled,(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_unused_releases),(SELECT count(*) FROM budget_unused_release_scopes) FROM budget_actions WHERE id=$1`, p.ID).Scan(&state, &settled, &holds, &proofs, &links); err != nil || state != "not_executed" || settled || holds != 3 || proofs != 1 || links != 3 {
		t.Fatalf("partial/forged model settlement: %s %v %d %d %d %v", state, settled, holds, proofs, links, err)
	}
	if _, err = a.BeginPrepared(ctx, claim); err == nil {
		t.Fatal("released action obtained sending authority")
	}
	db, err := OpenDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restarted, err := NewAuthority(db, a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := restarted.ReleaseCancelledPrepared(ctx, p.ID)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatalf("release replay changed proof: %+v %v", again, err)
	}
	againCancel, err := restarted.CancelAttempt(ctx, actor, c)
	if err != nil || !reflect.DeepEqual(cancelReceipt, againCancel) {
		t.Fatalf("release rewrote cancellation receipt: %+v %v", againCancel, err)
	}
	v, err := a.OperatorAttempt(ctx, actor, c.AttemptID)
	if err != nil || v.ControlVersion != 3 || v.InboxState != "cancelled" || v.ReleasedUnusedActions != 1 || v.RetainedModelReservations != 0 {
		t.Fatalf("release revived/staled attempt: %+v %v", v, err)
	}
}

func TestUnusedReleaseAutomaticRecoveryIsBoundedScopedAndDoesNotResumeJobs(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverCancelledPrepared(ctx, 8); err != nil || n != 0 {
		t.Fatalf("active preparation released: %d %v", n, err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	other := *a
	other.cfg.ProjectID = "other-project"
	if n, err := other.RecoverCancelledPrepared(ctx, 8); err != nil || n != 0 {
		t.Fatalf("cross-project release: %d %v", n, err)
	}
	for _, limit := range []int{0, 129} {
		if _, err := a.RecoverCancelledPrepared(ctx, limit); err == nil {
			t.Fatal("unbounded release recovery accepted")
		}
	}
	if n, err := a.RecoverCancelledPrepared(ctx, 1); err != nil || n != 1 {
		t.Fatalf("recovery: %d %v", n, err)
	}
	if n, err := a.RecoverCancelledPrepared(ctx, 8); err != nil || n != 0 {
		t.Fatalf("released action reprocessed: %d %v", n, err)
	}
	var state string
	var marker bool
	if err = a.db.QueryRow(`SELECT a.state,l.sending_fence IS NOT NULL FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.id=$1`, p.ID).Scan(&state, &marker); err != nil || state != "not_executed" || marker {
		t.Fatalf("recovery sent work: %s %v %v", state, marker, err)
	}
}

func TestUnusedReleaseRestoresExactlyTheUnusedCapacityInEveryScope(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	binding, err := a.binding(i)
	if err != nil {
		t.Fatal(err)
	}
	check := func(released bool) {
		t.Helper()
		tx, err := a.begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var now time.Time
		if err = tx.QueryRow(`SELECT clock_timestamp()`).Scan(&now); err != nil {
			t.Fatal(err)
		}
		for _, ref := range a.scopeRefs(i, binding) {
			if ref.kind != "attempt" {
				ref.origin, err = periodID(ref.limit.Period, now)
				if err != nil {
					t.Fatal(err)
				}
			}
			request := ref.limit
			request.Period = nil
			err = checkCapacity(ctx, tx, ref, request)
			if released && err != nil {
				t.Fatalf("%s unused capacity not released: %v", ref.kind, err)
			}
			if !released && !errors.Is(err, ErrBudgetExhausted) {
				t.Fatalf("%s reservation was not enforced: %v", ref.kind, err)
			}
		}
	}
	check(false)
	if _, err = a.ReleaseCancelledPrepared(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	check(true)
}

func TestUnusedReleaseNeverRefundsSendingOrRecordedUsage(t *testing.T) {
	for _, stage := range []string{"sending", "uncertain", "recorded", "settled"} {
		t.Run(stage, func(t *testing.T) {
			a, i, actor, c, _ := cancellationFixture(t)
			ctx := context.Background()
			p, err := a.PrepareAction(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := a.ClaimPrepared(ctx, p, "worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			permit, err := a.BeginPrepared(ctx, claim)
			if err != nil {
				t.Fatal(err)
			}
			if stage != "sending" {
				body := []byte("recorded usage")
				if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{ResponseReceived: stage != "uncertain", Response: body, ResponseDigest: hash(body), ElapsedMS: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "settled" {
				if err = reconciler(a).SettleFinal(ctx, finalReceipt(i)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
				t.Fatal(err)
			}
			if r, err := a.ReleaseCancelledPrepared(ctx, p.ID); !errors.Is(err, ErrUnusedReleaseUnavailable) || r != nil {
				t.Fatalf("%s refunded: %+v %v", stage, r, err)
			}
			var holds int
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil || holds != 3 {
				t.Fatalf("%s holds lost: %d %v", stage, holds, err)
			}
		})
	}
}

func TestUnusedReleaseRejectsIncompleteOrContradictoryEvidence(t *testing.T) {
	for _, mutate := range []string{
		`DELETE FROM budget_action_leases`,
		`DELETE FROM budget_holds WHERE scope_id LIKE 'change:%'`,
		`UPDATE budget_actions SET intent_digest='corrupt'`,
		`UPDATE budget_action_leases SET sending_fence=fence`,
		`UPDATE budget_action_leases SET job_token='ownerless-epoch'`,
		`UPDATE budget_actions SET integrity_incident=true`,
		`UPDATE budget_operator_commands SET receipt_digest='corrupt'`,
	} {
		t.Run(mutate, func(t *testing.T) {
			a, i, actor, c, _ := cancellationFixture(t)
			ctx := context.Background()
			p, err := a.PrepareAction(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
				t.Fatal(err)
			}
			if _, err = a.db.Exec(mutate); err != nil {
				t.Fatal(err)
			}
			if r, err := a.ReleaseCancelledPrepared(ctx, p.ID); err == nil || r != nil {
				t.Fatalf("unproven action refunded: %+v %v", r, err)
			}
			var proofs int
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_unused_releases`).Scan(&proofs); err != nil || proofs != 0 {
				t.Fatalf("failed proof persisted: %d %v", proofs, err)
			}
		})
	}
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	if err = reconciler(a).SettleFinal(ctx, finalReceipt(i)); err == nil {
		t.Fatal("receipt before sending accepted")
	}
	if r, err := a.ReleaseCancelledPrepared(ctx, p.ID); err == nil || r != nil {
		t.Fatalf("quarantined usage claim ignored: %+v %v", r, err)
	}
}

func TestUnusedReleaseCommitFailureAndDuplicateWorkersCannotDoubleRefund(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`CREATE FUNCTION fail_unused_release() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected release commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_unused_release AFTER INSERT ON budget_unused_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_unused_release()`); err != nil {
		t.Fatal(err)
	}
	if r, err := a.ReleaseCancelledPrepared(ctx, p.ID); err == nil || r != nil {
		t.Fatalf("uncommitted release escaped: %+v %v", r, err)
	}
	var holds, proofs, links, version int
	if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_unused_releases),(SELECT count(*) FROM budget_unused_release_scopes),control_version FROM budget_jobs`).Scan(&holds, &proofs, &links, &version); err != nil || holds != 3 || proofs != 0 || links != 0 || version != 2 {
		t.Fatalf("partial refund: %d %d %d %d %v", holds, proofs, links, version, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_unused_release ON budget_unused_releases`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	receipts := make(chan *UnusedReleaseReceipt, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := a.ReleaseCancelledPrepared(ctx, p.ID)
			if err != nil {
				t.Error(err)
				return
			}
			receipts <- r
		}()
	}
	wg.Wait()
	close(receipts)
	var first *UnusedReleaseReceipt
	for r := range receipts {
		if first == nil {
			first = r
		}
		if !reflect.DeepEqual(first, r) {
			t.Error("duplicate release changed receipt")
		}
	}
	if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_unused_releases),control_version FROM budget_jobs`).Scan(&holds, &proofs, &version); err != nil || holds != 3 || proofs != 1 || version != 3 {
		t.Fatalf("duplicate refund: %d %d %d %v", holds, proofs, version, err)
	}
}

func TestUnusedReleaseProofCorruptionBlocksEveryScopeInsteadOfErasingCharges(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReleaseCancelledPrepared(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_unused_releases SET receipt_digest='corrupt'`); err != nil {
		t.Fatal(err)
	}
	binding, err := a.binding(i)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, ref := range a.scopeRefs(i, binding) {
		if err = checkCapacity(ctx, tx, ref, review.ResourceLimitScope{}); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("%s spent without release proof: %v", ref.kind, err)
		}
	}
}

func TestUnusedReleaseDamagedAttributionCannotSilentlyRemoveAScopeCharge(t *testing.T) {
	for _, fault := range []string{
		`DELETE FROM budget_holds WHERE scope_id LIKE 'project:%'`,
		`DELETE FROM budget_unused_release_scopes WHERE scope_id LIKE 'project:%'`,
		`UPDATE budget_action_leases SET sending_fence=fence`,
		`UPDATE budget_actions SET quote=convert_to('corrupt','UTF8')`,
		`UPDATE budget_operator_commands SET receipt_digest='corrupt'`,
	} {
		t.Run(fault, func(t *testing.T) {
			a, i, actor, c, _ := cancellationFixture(t)
			ctx := context.Background()
			p, err := a.PrepareAction(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
				t.Fatal(err)
			}
			if _, err = a.ReleaseCancelledPrepared(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = a.db.Exec(fault); err != nil {
				t.Fatal(err)
			}
			binding, err := a.binding(i)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := a.begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			for _, ref := range a.scopeRefs(i, binding) {
				if err = checkCapacity(ctx, tx, ref, review.ResourceLimitScope{}); !errors.Is(err, ErrIntegrity) {
					t.Fatalf("%s silently removed financial attribution: %v", ref.kind, err)
				}
			}
		})
	}
}

func TestUnusedReleaseContradictoryFinalUsageBlocksAllReleasedScopes(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReleaseCancelledPrepared(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = reconciler(a).SettleFinal(ctx, finalReceipt(i)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("contradictory usage did not fail closed: %v", err)
	}
	binding, err := a.binding(i)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, ref := range a.scopeRefs(i, binding) {
		if err = checkCapacity(ctx, tx, ref, review.ResourceLimitScope{}); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("%s released-scope incident ignored: %v", ref.kind, err)
		}
	}
}

func TestUnusedReleasePreservesKnownSnapshotChargesAndOtherModelHolds(t *testing.T) {
	a, parent := toolChildParentFixture(t)
	ctx := context.Background()
	actor, c := cancellationActor(t, a, parent)
	child, err := a.AdvanceModelToolContinuation(ctx, parent.ActionID, pipeline.ModelToolContinuationV1{})
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = a.db.QueryRow(`SELECT md5(receipt) FROM budget_model_tool_observations`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReleaseCancelledPrepared(ctx, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReleaseCancelledPrepared(ctx, parent.ActionID); !errors.Is(err, ErrUnusedReleaseUnavailable) {
		t.Fatalf("recorded parent refunded: %v", err)
	}
	var after string
	var holds, charges, proofs int
	if err = a.db.QueryRow(`SELECT md5(receipt),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_tool_observation_scopes),(SELECT count(*) FROM budget_unused_releases) FROM budget_model_tool_observations`).Scan(&after, &holds, &charges, &proofs); err != nil || before != after || holds != 6 || charges != 3 || proofs != 1 {
		t.Fatalf("release erased other charges/provenance: %s %s %d %d %d %v", before, after, holds, charges, proofs, err)
	}
	v, err := a.OperatorAttempt(ctx, actor, c.AttemptID)
	if err != nil || v.ReleasedUnusedActions != 1 || v.RetainedModelReservations != 1 {
		t.Fatalf("wrong effective reservations: %+v %v", v, err)
	}
}

func TestUnusedReleaseUsesFrozenQualificationAndNotCurrentCredentialsOrDeadline(t *testing.T) {
	a, i, actor, c, _ := cancellationFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
		t.Fatal(err)
	}
	a.cfg.Catalog, _ = NewCatalog(nil)
	a.cfg.Operators = nil
	a.cfg.Limits.Attempt.ModelCalls = 0
	if _, err = a.db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ReleaseCancelledPrepared(ctx, p.ID); err != nil {
		t.Fatalf("frozen non-execution proof required new dispatch credentials: %v", err)
	}
}

func TestUnusedReleaseRecoveryFailsClosedWhenItsJournalIsUnavailable(t *testing.T) {
	a, _, _, _, _ := cancellationFixture(t)
	if _, err := a.db.Exec(`ALTER TABLE budget_unused_releases RENAME TO unavailable_unused_releases`); err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverCancelledPrepared(context.Background(), 8); err == nil || n != 0 {
		t.Fatalf("missing journal looked like an empty recovery queue: %d %v", n, err)
	}
}

func TestUnusedReleaseCrashChild(t *testing.T) {
	dsn := os.Getenv("SEVENREVIEW_UNUSED_RELEASE_DSN")
	if dsn == "" {
		t.Skip("subprocess helper")
	}
	db, err := OpenDatabase(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	id := os.Getenv("SEVENREVIEW_UNUSED_RELEASE_ACTION")
	if _, err = db.Exec(`SELECT set_config('application_name',$1,false)`, "unused-"+id); err != nil {
		t.Fatal(err)
	}
	a := ledgerAuthority(t, db, ledgerLimits())
	if _, err = a.ReleaseCancelledPrepared(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	os.Exit(73)
}

func TestUnusedReleaseProcessDeathAndUnknownCommitDoNotDoubleCredit(t *testing.T) {
	for _, stage := range []string{"before_commit", "commit_ack_unknown", "after_commit"} {
		t.Run(stage, func(t *testing.T) {
			a, i, actor, c, dsn := cancellationFixture(t)
			ctx := context.Background()
			p, err := a.PrepareAction(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.CancelAttempt(ctx, actor, c); err != nil {
				t.Fatal(err)
			}
			lock, err := a.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if stage != "after_commit" {
				if _, err = lock.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext(current_schema()))`); err != nil {
					t.Fatal(err)
				}
				defer lock.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext(current_schema()))`)
				if _, err = a.db.Exec(`CREATE FUNCTION pause_unused_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(TG_TABLE_SCHEMA)); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER pause_unused_commit AFTER INSERT ON budget_unused_releases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pause_unused_commit()`); err != nil {
					t.Fatal(err)
				}
			}
			child := exec.Command(os.Args[0], "-test.run=^TestUnusedReleaseCrashChild$")
			child.Env = append(os.Environ(), "SEVENREVIEW_UNUSED_RELEASE_DSN="+dsn, "SEVENREVIEW_UNUSED_RELEASE_ACTION="+p.ID)
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			defer child.Process.Kill()
			if stage != "after_commit" {
				waiting := false
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if err = a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory')`, "unused-"+p.ID).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !waiting {
					t.Fatal("release child did not reach commit barrier")
				}
				if err = child.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				_ = child.Wait()
				if stage == "before_commit" {
					var terminated bool
					if err = a.db.QueryRow(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory'`, "unused-"+p.ID).Scan(&terminated); err != nil || !terminated {
						t.Fatalf("could not establish rollback: %v", err)
					}
				}
				if _, err = lock.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext(current_schema()))`); err != nil {
					t.Fatal(err)
				}
				if stage == "before_commit" {
					var count int
					if err = a.db.QueryRow(`SELECT count(*) FROM budget_unused_releases`).Scan(&count); err != nil || count != 0 {
						t.Fatalf("uncommitted proof survived: %d %v", count, err)
					}
				}
			} else {
				err = child.Wait()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 73 {
					t.Fatalf("child failed before commit: %v", err)
				}
			}
			r, err := a.ReleaseCancelledPrepared(ctx, p.ID)
			if err != nil || r.ControlVersion != 3 {
				t.Fatalf("release reconciliation: %+v %v", r, err)
			}
			var proofs, holds int
			var version uint64
			if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_unused_releases),(SELECT count(*) FROM budget_holds),control_version FROM budget_jobs`).Scan(&proofs, &holds, &version); err != nil || proofs != 1 || holds != 3 || version != 3 {
				t.Fatalf("duplicate/lost credit: %d %d %d %v", proofs, holds, version, err)
			}
		})
	}
}
