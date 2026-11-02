package accounting

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
)

func TestPostgresV9UpgradePreservesPreparedSendingAndLegacyAuthority(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	ctx := context.Background()
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := a.ClaimPrepared(ctx, p, "live-prepared", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fencedIntent := i
	fencedIntent.ActionID = "fenced-sending"
	fp, err := a.PrepareAction(ctx, fencedIntent)
	if err != nil {
		t.Fatal(err)
	}
	fc, err := a.ClaimPrepared(ctx, fp, "live-sending", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, fc)
	if err != nil {
		t.Fatal(err)
	}
	legacy := i
	legacy.ActionID = "historical-unfenced"
	legacyPermit, err := a.ReserveAndBegin(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() []byte {
		t.Helper()
		var raw []byte
		// Exclude only the two new v9 columns. All immutable bytes, states,
		// tokens, lease epochs, holds and original deadlines must survive.
		err := a.db.QueryRow(`SELECT json_build_object('actions',(SELECT json_agg(x ORDER BY id) FROM (SELECT id,request,quote,upper_amount,state,outcome FROM budget_actions) x),'holds',(SELECT json_agg(h ORDER BY action_id,scope_id) FROM budget_holds h),'leases',(SELECT json_agg(l ORDER BY action_id) FROM budget_action_leases l),'inbox',(SELECT json_agg(i ORDER BY run_id) FROM budget_inbox i),'jobs',(SELECT json_agg(x ORDER BY run_id) FROM (SELECT run_id,attempt_id,dispatch_allowed,phase FROM budget_jobs) x))`).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err = json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	if _, err = a.db.Exec(`ALTER TABLE budget_actions DROP COLUMN fenced; ALTER TABLE budget_jobs DROP COLUMN recovery_action_id; UPDATE budget_schema SET version=8`); err != nil {
		t.Fatal(err)
	}
	if tx, err := a.begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("runtime implicitly accepted old schema")
	}
	// Commit failure must roll back both DDL and provenance backfill.
	if _, err = a.db.Exec(`CREATE FUNCTION fail_v9_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=9 THEN RAISE EXCEPTION 'injected migration commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_v9_commit AFTER UPDATE ON budget_schema DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_v9_commit()`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, a.db); err == nil {
		t.Fatal("uncommitted migration reported success")
	}
	var version, columns int
	if err = a.db.QueryRow(`SELECT version FROM budget_schema`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = a.db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND ((table_name='budget_actions' AND column_name='fenced') OR (table_name='budget_jobs' AND column_name='recovery_action_id'))`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if version != 8 || columns != 0 || !bytes.Equal(before, snapshot()) {
		t.Fatal("failed upgrade partially committed or changed authority")
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_v9_commit ON budget_schema`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, a.db); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, a.db); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
	if !bytes.Equal(before, snapshot()) {
		t.Fatal("upgrade rewrote immutable requests, tokens, holds or deadlines")
	}
	var fenced, unfenced int
	if err = a.db.QueryRow(`SELECT count(*) FILTER(WHERE fenced),count(*) FILTER(WHERE NOT fenced) FROM budget_actions`).Scan(&fenced, &unfenced); err != nil {
		t.Fatal(err)
	}
	if fenced != 2 || unfenced != 1 {
		t.Fatalf("incorrect provenance backfill: %d %d", fenced, unfenced)
	}
	if _, err = a.BeginPrepared(ctx, prepared); err != nil {
		t.Fatalf("upgrade invalidated live prepared lease: %v", err)
	}
	response := []byte("known response")
	out := orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}
	forged := permit
	forged.Fence = 0
	forged.WorkerID = ""
	if err = a.RecordOutcome(ctx, forged, out); err == nil {
		t.Fatal("upgrade downgraded fenced sending authority")
	}
	if err = a.RecordOutcome(ctx, permit, out); err != nil {
		t.Fatalf("upgrade lost sending owner: %v", err)
	}
	if err = a.RecordOutcome(ctx, legacyPermit, out); err != nil {
		t.Fatalf("upgrade fabricated a fence for historical sending: %v", err)
	}
	if candidates, err := a.PreparedRecoveryCandidates(ctx, 8); err != nil || len(candidates) != 0 {
		t.Fatalf("upgrade made sending executable: %#v %v", candidates, err)
	}
}
