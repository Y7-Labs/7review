package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
)

func TestPreparedActionCrashChild(t *testing.T) {
	stage := os.Getenv("SEVENREVIEW_PREPARED_CRASH_STAGE")
	if stage == "" {
		t.Skip("subprocess helper")
	}
	db, err := sql.Open("pgx", os.Getenv("SEVENREVIEW_PREPARED_CRASH_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	a := ledgerAuthority(t, db, ledgerLimits())
	var i orchestrator.DispatchIntent
	if err = json.Unmarshal([]byte(os.Getenv("SEVENREVIEW_PREPARED_CRASH_INTENT")), &i); err != nil {
		t.Fatal(err)
	}
	p, err := a.PrepareAction(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	if stage != "prepare" {
		duration := 150 * time.Millisecond
		if stage == "send" {
			duration = time.Minute
		}
		claim, e := a.ClaimPrepared(context.Background(), p, "dead-worker", duration)
		if e != nil {
			t.Fatal(e)
		}
		if stage == "send" {
			if _, e = a.BeginPrepared(context.Background(), claim); e != nil {
				t.Fatal(e)
			}
		}
	}
	// No callback or deferred cleanup can run after this committed boundary.
	os.Exit(67)
}

// REC-01/REC-02, S15: OS process loss must not erase a reservation, let an old
// owner win after reclaim, or turn a committed sending marker into retryability.
func TestScenarioS15PreparedProcessDeathAndStaleOwnerRace(t *testing.T) {
	for _, stage := range []string{"prepare", "claim", "send"} {
		t.Run(stage, func(t *testing.T) {
			a, i, dsn := preparedActionFixture(t)
			raw, _ := json.Marshal(i)
			cmd := exec.Command(os.Args[0], "-test.run=^TestPreparedActionCrashChild$")
			cmd.Env = append(os.Environ(), "SEVENREVIEW_PREPARED_CRASH_STAGE="+stage, "SEVENREVIEW_PREPARED_CRASH_DSN="+dsn, "SEVENREVIEW_PREPARED_CRASH_INTENT="+string(raw))
			err := cmd.Run()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 67 {
				t.Fatalf("child did not reach %s commit boundary: %v", stage, err)
			}
			reopened := ledgerAuthority(t, a.db, ledgerLimits())
			ctx := context.Background()
			var old orchestrator.ActionClaim
			if stage != "prepare" {
				old = orchestrator.ActionClaim{ID: i.ActionID, RequestDigest: i.RequestDigest, Fence: 1, WorkerID: "dead-worker", Job: i.Job}
				if err = a.db.QueryRow(`SELECT lease_until FROM budget_action_leases WHERE action_id=$1`, i.ActionID).Scan(&old.LeaseUntil); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "send" {
				if _, err = a.db.Exec(`UPDATE budget_action_leases SET lease_until=clock_timestamp()-interval '1 second' WHERE action_id=$1`, i.ActionID); err != nil {
					t.Fatal(err)
				}
				candidates, e := reopened.PreparedRecoveryCandidates(ctx, 8)
				if e != nil || len(candidates) != 0 {
					t.Fatalf("sending action advertised for reclaim: %#v %v", candidates, e)
				}
				if _, e = reopened.ReclaimPrepared(ctx, orchestrator.PreparedAction{ID: i.ActionID, RequestDigest: i.RequestDigest, Fence: 1}, "new-worker", time.Minute); e == nil {
					t.Fatal("sending process death authorized a new sender")
				}
			} else {
				if stage == "claim" {
					// Wait on the authoritative database lease, not an assumed client sleep.
					if _, err = a.db.Exec(`SELECT pg_sleep(greatest(0,extract(epoch FROM (lease_until-clock_timestamp())))+0.002) FROM budget_action_leases WHERE action_id=$1`, i.ActionID); err != nil {
						t.Fatal(err)
					}
				}
				candidates, e := reopened.PreparedRecoveryCandidates(ctx, 8)
				if e != nil || len(candidates) != 1 {
					t.Fatalf("lost prepared receipt not discoverable: %#v %v", candidates, e)
				}
				var calls atomic.Int32
				var wg sync.WaitGroup
				if stage == "claim" {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if p, e := a.BeginPrepared(ctx, old); e == nil && !p.AlreadyStarted {
							calls.Add(1)
							t.Error("stale process acquired a fresh sending permit")
						}
					}()
				}
				claim, e := reopened.ReclaimPrepared(ctx, candidates[0], "recovery-worker", time.Minute)
				if e != nil {
					t.Fatal(e)
				}
				if claim.Intent == nil || !bytes.Equal(claim.Intent.Payload, i.Payload) || claim.Intent.Provider != i.Provider || claim.Intent.Model != i.Model {
					t.Fatal("restart reconstructed a different provider request")
				}
				permit, e := reopened.BeginPrepared(ctx, claim)
				if e != nil || permit.AlreadyStarted {
					t.Fatalf("prepared recovery did not begin once: %#v %v", permit, e)
				}
				calls.Add(1) // Controlled provider effect happens only after a fresh permit.
				replay, e := reopened.BeginPrepared(ctx, claim)
				if e != nil || !replay.AlreadyStarted {
					t.Fatalf("lost send receipt authorized resend: %#v %v", replay, e)
				}
				wg.Wait()
				if calls.Load() != 1 {
					t.Fatalf("provider effects=%d", calls.Load())
				}
				response := []byte("controlled response")
				if e = reopened.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: hash(response), ElapsedMS: 1}); e != nil {
					t.Fatal(e)
				}
				if p, e := a.PrepareAction(ctx, i); e == nil && !p.AlreadyStarted {
					t.Fatalf("stale job process could prepare again: %#v", p)
				}
			}
			var actions, holds, settled int
			if err = a.db.QueryRow(`SELECT count(*),count(*) FILTER(WHERE settled) FROM budget_actions`).Scan(&actions, &settled); err != nil {
				t.Fatal(err)
			}
			if err = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds); err != nil {
				t.Fatal(err)
			}
			if actions != 1 || holds != 3 || settled != 0 {
				t.Fatalf("process loss reset accounting: actions=%d holds=%d settled=%d", actions, holds, settled)
			}
		})
	}
}
