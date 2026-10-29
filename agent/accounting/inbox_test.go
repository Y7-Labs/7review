package accounting

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func TestPostgresInboxCommitReplayConflictAndRecovery(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	ctx := context.Background()
	req := review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "delivery", PayloadDigest: "sha256:" + string64("a")}
	first, err := a.AcceptInbox(ctx, req, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate loss of the HTTP response and process-local state.
	reopened := ledgerAuthority(t, db, ledgerLimits())
	replay, err := reopened.AcceptInbox(ctx, req, time.Hour)
	if err != nil || replay.RunID != first.RunID || !replay.Deadline.Equal(first.Deadline) {
		t.Fatalf("replay changed receipt: %#v %v", replay, err)
	}
	req.Title = "changed normalized request"
	if _, err = a.AcceptInbox(ctx, req, time.Minute); err == nil {
		t.Fatal("same raw digest concealed request conflict")
	}
	req.Title = ""
	req.PayloadDigest = "sha256:" + string64("b")
	if _, err = a.AcceptInbox(ctx, req, time.Minute); err == nil {
		t.Fatal("changed payload accepted")
	}
	claimed, err := reopened.ClaimInbox(ctx)
	if err != nil || claimed.RunID != first.RunID || claimed.Request.DeliveryID != "delivery" {
		t.Fatalf("pending work lost on reopen: %#v %v", claimed, err)
	}
	if next, err := a.ClaimInbox(ctx); err != nil || next != nil {
		t.Fatalf("claimed work was redispatched: %#v %v", next, err)
	}
	if err = a.FinishInbox(ctx, first.RunID, "wrong-token", true); err == nil {
		t.Fatal("unowned completion accepted")
	}
	if err = a.FinishInbox(ctx, first.RunID, claimed.ClaimToken, false); err != nil {
		t.Fatal(err)
	}
	replay, err = reopened.AcceptInbox(ctx, claimed.Request, time.Minute)
	if err != nil || replay.State != "failed" {
		t.Fatalf("terminal replay lost state: %#v %v", replay, err)
	}
	var conflicts int
	if err = db.QueryRow(`SELECT count(*) FROM budget_inbox_conflicts`).Scan(&conflicts); err != nil || conflicts != 2 {
		t.Fatalf("conflicts not durable: %d %v", conflicts, err)
	}
}

func TestPostgresInboxConcurrentClaimsAndUncertainCrash(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	ctx := context.Background()
	req := review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "delivery"}
	receipt, err := a.AcceptInbox(ctx, req, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *InboxItem, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := a.ClaimInbox(ctx)
			if e != nil {
				t.Error(e)
			}
			results <- v
		}()
	}
	wg.Wait()
	close(results)
	count := 0
	for v := range results {
		if v != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("claims=%d", count)
	}
	// A crash after claim has unknown effects. Expiry must not requeue it.
	if _, err = db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 second' WHERE run_id=$1`, receipt.RunID); err != nil {
		t.Fatal(err)
	}
	if v, e := a.ClaimInbox(ctx); e != nil || v != nil {
		t.Fatalf("uncertain work reclaimed: %#v %v", v, e)
	}
	replay, err := a.AcceptInbox(ctx, req, time.Hour)
	if err != nil || replay.State != "uncertain" {
		t.Fatalf("crash not visible: %#v %v", replay, err)
	}
}

func string64(s string) string {
	var out string
	for n := 0; n < 64; n++ {
		out += s
	}
	return out
}

func TestPostgresInboxFailedCommitCannotAcknowledgeOrLoseWork(t *testing.T) {
	db, _ := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	ctx := context.Background()
	// Fail at transaction commit, after INSERT and receipt selection succeeded.
	_, err := db.Exec(`CREATE FUNCTION fail_inbox_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_inbox_commit AFTER INSERT ON budget_inbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_inbox_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	req := review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DeliveryID: "delivery"}
	if receipt, err := a.AcceptInbox(ctx, req, time.Minute); err == nil || receipt != nil {
		t.Fatalf("uncommitted work acknowledged: %#v %v", receipt, err)
	}
	if _, err = db.Exec(`DROP TRIGGER fail_inbox_commit ON budget_inbox`); err != nil {
		t.Fatal(err)
	}
	receipt, err := a.AcceptInbox(ctx, req, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 second' WHERE run_id=$1`, receipt.RunID); err != nil {
		t.Fatal(err)
	}
	if next, err := a.ClaimInbox(ctx); err != nil || next != nil {
		t.Fatalf("expired acceptance got fresh deadline: %#v %v", next, err)
	}
	replay, err := a.AcceptInbox(ctx, req, time.Hour)
	if err != nil || replay.State != "expired" {
		t.Fatalf("expired acceptance revived: %#v %v", replay, err)
	}
}
