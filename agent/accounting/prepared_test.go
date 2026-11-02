package accounting

import (
	"context"
	"testing"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func preparedActionFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent, string) {
	t.Helper()
	db, dsn := postgresFixture(t)
	a := ledgerAuthority(t, db, ledgerLimits())
	req, input, source := preparedAggregate(t, a)
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
	i := dispatchForReceipt(ledgerIntent(t, db, "prepared-fixture", "7", ledgerLimits()), aggregate.Receipt)
	i.Job = &claim
	return a, i, dsn
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
