package accounting

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func preparedActionFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent, string) {
	t.Helper()
	return preparedActionFixtureWithLimits(t, ledgerLimits())
}

func preparedActionFixtureWithLimits(t *testing.T, limits review.ResourceLimits) (*Authority, orchestrator.DispatchIntent, string) {
	t.Helper()
	db, dsn := postgresFixture(t)
	a := ledgerAuthority(t, db, limits)
	req, input, source := preparedAggregate(t, a)
	input.Policy.Limits = &limits
	input.Policy.OperatorCeilings = &limits
	source.Policy = input.Policy
	if _, err := a.AcceptAggregate(context.Background(), req, input, source, nil); err != nil {
		t.Fatal(err)
	}
	job, err := a.ClaimInbox(context.Background())
	if err != nil || job == nil {
		t.Fatalf("job: %#v %v", job, err)
	}
	claim := review.JobClaim{RunID: job.RunID, Token: job.ClaimToken}
	aggregate, err := a.LoadAggregate(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	i := dispatchForReceipt(ledgerIntent(t, db, "prepared-fixture", "7", limits), aggregate.Receipt)
	i.Job = &claim
	return a, i, dsn
}

func TestRecoveredActionClaimCannotFinishWholeInbox(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "success"}[success], func(t *testing.T) {
			a, i, _ := preparedActionFixture(t)
			ctx := context.Background()
			prepared, err := a.PrepareAction(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := a.ReclaimPrepared(ctx, prepared, "action-only-owner", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.FinishInbox(ctx, claim.Job.RunID, claim.Job.Token, success); err == nil {
				t.Fatal("action-only recovery token finished whole job")
			}
			var state, phase string
			if err = a.db.QueryRow(`SELECT i.state,j.phase FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&state, &phase); err != nil || state != "running" || phase != "admitted" {
				t.Fatalf("rejected completion changed authority: %s %s %v", state, phase, err)
			}
		})
	}
}

func TestPostgresPreparedLeaseConcurrencyAndCommitFaults(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db.Exec(`CREATE FUNCTION fail_action_claim() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected claim commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_action_claim AFTER UPDATE ON budget_action_leases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_action_claim()`)
	if err != nil {
		t.Fatal(err)
	}
	if claim, err := a.ClaimPrepared(ctx, p, "failed-worker", time.Minute); err == nil || claim.Fence != 0 {
		t.Fatalf("uncommitted claim escaped: %#v %v", claim, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_action_claim ON budget_action_leases`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan orchestrator.ActionClaim, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := a.ClaimPrepared(ctx, p, "contender", time.Minute)
			if e == nil {
				claims <- c
			}
		}()
	}
	wg.Wait()
	close(claims)
	var winner orchestrator.ActionClaim
	count := 0
	for c := range claims {
		winner = c
		count++
	}
	if count != 1 || winner.Fence != 1 {
		t.Fatalf("claims=%d winner=%#v", count, winner)
	}
	_, err = a.db.Exec(`CREATE FUNCTION fail_prepared_send() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='sending' THEN RAISE EXCEPTION 'injected sending commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_prepared_send AFTER UPDATE ON budget_actions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_prepared_send()`)
	if err != nil {
		t.Fatal(err)
	}
	if permit, err := a.BeginPrepared(ctx, winner); err == nil || permit.ID != "" {
		t.Fatalf("uncommitted send permit escaped: %#v %v", permit, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_prepared_send ON budget_actions`); err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, winner)
	if err != nil || permit.AlreadyStarted {
		t.Fatalf("rolled-back sending could not resume: %#v %v", permit, err)
	}
	response := []byte("late result")
	forged := permit
	forged.Fence = 0
	out := orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}
	if err = a.RecordOutcome(ctx, forged, out); err == nil {
		t.Fatal("unfenced caller recorded a fenced outcome")
	}
	if _, err = a.db.Exec(`UPDATE budget_action_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE action_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.RecordOutcome(ctx, permit, out); err != nil {
		t.Fatalf("sending lease expiry discarded attributable response: %v", err)
	}
}

func TestPostgresBeginPreparedRechecksAuthorityAndOriginalReservation(t *testing.T) {
	for _, name := range []string{"closed", "obsolete", "deadline", "qualification", "route registry", "hold missing", "scope authority", "integrity", "job finished"} {
		t.Run(name, func(t *testing.T) {
			a, i, _ := preparedActionFixture(t)
			ctx := context.Background()
			p, err := a.PrepareAction(ctx, i)
			if err != nil {
				t.Fatal(err)
			}
			claim, err := a.ClaimPrepared(ctx, p, "owner", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "closed":
				err = a.CloseAttempt(ctx, i.Attempt.AttemptID)
			case "obsolete":
				_, err = a.db.Exec(`UPDATE budget_heads SET current_attempt_id=NULL`)
			case "deadline":
				_, err = a.db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 second'`)
			case "qualification":
				a.cfg.Catalog, _ = NewCatalog(nil)
			case "route registry":
				_, err = a.db.Exec(`DELETE FROM budget_routes`)
			case "hold missing":
				_, err = a.db.Exec(`DELETE FROM budget_holds WHERE scope_id LIKE 'project:%'`)
			case "scope authority":
				_, err = a.db.Exec(`UPDATE budget_scopes SET authority_digest='changed' WHERE kind='project'`)
			case "integrity":
				_, err = a.db.Exec(`UPDATE budget_actions SET integrity_incident=true`)
			case "job finished":
				// Simulate a legacy terminal row. Ordinary live completion can no
				// longer create this state once a preparation is committed.
				_, err = a.db.Exec(`UPDATE budget_jobs SET phase='finished',dispatch_allowed=false; UPDATE budget_inbox SET state='failed'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if permit, err := a.BeginPrepared(ctx, claim); err == nil || permit.ID != "" {
				t.Fatalf("%s failed open: %#v %v", name, permit, err)
			}
			var state string
			_ = a.db.QueryRow(`SELECT state FROM budget_actions WHERE id=$1`, p.ID).Scan(&state)
			if state != "prepared" {
				t.Fatalf("failed authority check committed sending: %s", state)
			}
		})
	}
}

func TestPostgresRecoveryDoesNotTakeOverJobWithExternalProgress(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	other := i
	other.ActionID = "parallel-prepared"
	second, err := a.PrepareAction(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	firstClaim, err := a.ClaimPrepared(ctx, p, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondClaim, err := a.ClaimPrepared(ctx, second, "second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.BeginPrepared(ctx, firstClaim); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_action_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE action_id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	second.Fence = secondClaim.Fence
	if _, err = a.ReclaimPrepared(ctx, second, "recovery", time.Minute); err == nil {
		t.Fatal("recovery took over a job with possible external progress")
	}
	var token string
	if err = a.db.QueryRow(`SELECT claim_token FROM budget_inbox WHERE run_id=$1`, i.Job.RunID).Scan(&token); err != nil || token != i.Job.Token {
		t.Fatalf("failed recovery revoked active sender: %v", err)
	}
}

// REC-01: expiry permits a new prepared owner, never a second sending owner.
func TestPostgresPreparedReclaimFencesActionAndStaleJobOwner(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	old, err := a.ClaimPrepared(ctx, p, "worker-old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if old.Fence != 1 {
		t.Fatalf("first fencing epoch=%d", old.Fence)
	}
	p.Fence = old.Fence
	if _, err = a.ReclaimPrepared(ctx, p, "worker-new", time.Minute); err == nil {
		t.Fatal("live lease was stolen")
	}
	if _, err = a.db.Exec(`UPDATE budget_action_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE action_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.BeginPrepared(ctx, old); err == nil {
		t.Fatal("expired owner began sending")
	}
	_, err = a.db.Exec(`CREATE FUNCTION fail_reclaim_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected reclaim commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_reclaim_commit AFTER UPDATE ON budget_action_leases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_reclaim_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	if c, e := a.ReclaimPrepared(ctx, p, "failed-recovery", time.Minute); e == nil || c.Fence != 0 {
		t.Fatalf("uncommitted reclaim escaped: %#v %v", c, e)
	}
	var originalToken string
	if err = a.db.QueryRow(`SELECT claim_token FROM budget_inbox WHERE run_id=$1`, i.Job.RunID).Scan(&originalToken); err != nil || originalToken != i.Job.Token {
		t.Fatal("failed reclaim revoked original job")
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_reclaim_commit ON budget_action_leases`); err != nil {
		t.Fatal(err)
	}
	next, err := a.ReclaimPrepared(ctx, p, "worker-new", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next.Fence != 2 || next.Job == nil || next.Job.Token == i.Job.Token {
		t.Fatalf("reclaim did not fence job and action: %#v", next)
	}
	if next.Intent == nil || !bytes.Equal(next.Intent.Payload, i.Payload) || next.Intent.RequestDigest != i.RequestDigest {
		t.Fatal("reclaim did not recover immutable input bytes")
	}
	if _, err = a.BeginPrepared(ctx, old); err == nil {
		t.Fatal("stale fencing token began sending")
	}
	stale := i
	stale.ActionID = "old-owner-new-action"
	if _, err = a.PrepareAction(ctx, stale); err == nil {
		t.Fatal("stale job owner scheduled a different action")
	}
	stale.Job = next.Job
	if _, err = a.PrepareAction(ctx, stale); err == nil {
		t.Fatal("recovery token authorized whole-job replay instead of only its immutable action")
	}
	permit, err := a.BeginPrepared(ctx, next)
	if err != nil || permit.AlreadyStarted || permit.Fence != 2 {
		t.Fatalf("new owner could not begin: %#v %v", permit, err)
	}
	replay, err := a.BeginPrepared(ctx, next)
	if err != nil || !replay.AlreadyStarted {
		t.Fatalf("sending replay granted another permit: %#v %v", replay, err)
	}
	p.Fence = 2
	if _, err = a.ReclaimPrepared(ctx, p, "third-worker", time.Minute); err == nil {
		t.Fatal("sending action became reclaimable")
	}
	var actions, holds int
	_ = a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&actions)
	_ = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
	if actions != 1 || holds != 3 {
		t.Fatalf("reclaim reset reservations: actions=%d holds=%d", actions, holds)
	}
}

func TestPostgresPreparedDiscoveryIsScopedAndNotSendingAuthority(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	list, err := a.PreparedRecoveryCandidates(ctx, 8)
	if err != nil || len(list) != 1 || list[0].ID != p.ID || list[0].Fence != 0 {
		t.Fatalf("lost preparation cannot be found: %#v %v", list, err)
	}
	cfg := a.cfg
	cfg.ProjectID = "other-project"
	other, err := NewAuthority(a.db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := other.PreparedRecoveryCandidates(ctx, 8); err != nil || len(list) != 0 {
		t.Fatalf("cross-project prepared disclosure: %#v %v", list, err)
	}
	if _, err = other.ReclaimPrepared(ctx, p, "intruder", time.Minute); err == nil {
		t.Fatal("cross-project reclaim succeeded")
	}
	claim, err := a.ClaimPrepared(ctx, p, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := a.PreparedRecoveryCandidates(ctx, 8); err != nil || len(list) != 0 {
		t.Fatalf("owned prepared action offered for recovery: %#v %v", list, err)
	}
	permit, err := a.BeginPrepared(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`DELETE FROM budget_action_leases WHERE action_id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	permit.Fence = 0
	permit.WorkerID = ""
	response := []byte("forged fallback")
	if err = a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response)}); err == nil {
		t.Fatal("missing lease degraded to legacy outcome authorization")
	}
}

func TestPostgresDurableSendingFenceOverridesInconsistentPreparedState(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.ClaimPrepared(ctx, p, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.BeginPrepared(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_actions SET state='prepared' WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if permit, err := a.BeginPrepared(ctx, c); err == nil || permit.ID != "" {
		t.Fatalf("durable sending fence was ignored: %#v %v", permit, err)
	}
}

func TestPostgresSendingLeaseDoesNotReplaceQualifiedInferenceDeadline(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.ClaimPrepared(ctx, p, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !permit.NotAfter.Equal(i.Execution.DeadlineAt) || !permit.NotAfter.After(c.LeaseUntil) {
		t.Fatalf("prepared ownership lease silently shortened inference contract: permit=%s lease=%s", permit.NotAfter, c.LeaseUntil)
	}
}

// REC-01: prepared work is durable but never permission to invoke a provider.
func TestPostgresPreparationReservesOnceWithoutSending(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	p, err := a.PrepareAction(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.PrepareAction(context.Background(), i)
	if err != nil || again.ID != p.ID || again.Fence != 0 {
		t.Fatalf("prepare replay changed identity: %#v %v", again, err)
	}
	var state string
	var holds int
	if err = a.db.QueryRow(`SELECT state FROM budget_actions WHERE id=$1`, i.ActionID).Scan(&state); err != nil || state != "prepared" {
		t.Fatalf("preparation marked sending: %s %v", state, err)
	}
	if err = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil || holds != 3 {
		t.Fatalf("prepare replay changed holds: %d %v", holds, err)
	}
	if permit, err := a.ReserveAndBegin(context.Background(), i); err != nil || !permit.AlreadyStarted {
		t.Fatalf("compatibility API bypassed prepared ownership: %#v %v", permit, err)
	}
	i.Payload = append(i.Payload, ' ')
	if _, err = a.PrepareAction(context.Background(), i); err == nil {
		t.Fatal("prepared request identity was mutable")
	}
}

func TestPostgresPreparedActionCannotHaveOutcomeOrFinalUsage(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	p, err := a.PrepareAction(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	response := []byte("impossible before sending")
	if err = a.RecordOutcome(context.Background(), orchestrator.DispatchPermit{ID: p.ID, RequestDigest: p.RequestDigest}, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response)}); err == nil {
		t.Fatal("prepared action acquired a provider outcome")
	}
	if err = reconciler(a).SettleFinal(context.Background(), finalReceipt(i)); err == nil {
		t.Fatal("prepared action settled billable usage")
	}
	var state string
	var settled bool
	if err = a.db.QueryRow(`SELECT state,settled FROM budget_actions WHERE id=$1`, i.ActionID).Scan(&state, &settled); err != nil || state != "prepared" || settled {
		t.Fatalf("prepared action mutated: %s %t %v", state, settled, err)
	}
}
