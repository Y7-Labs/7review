package accounting

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Y4NN777/7review/agent/orchestrator"
)

type recoveredProvider struct {
	t              *testing.T
	a              *Authority
	i              orchestrator.DispatchIntent
	calls, streams atomic.Int32
}

func (p *recoveredProvider) Name() string { return p.i.Provider }
func (p *recoveredProvider) check(req orchestrator.LLMRequest) {
	p.t.Helper()
	raw, _ := json.Marshal(req)
	if !bytes.Equal(raw, p.i.Payload) {
		p.t.Error("recovery changed immutable request")
	}
	var sending int
	if err := p.a.db.QueryRow(`SELECT count(*) FROM budget_actions a JOIN budget_action_leases l ON l.action_id=a.id WHERE a.id=$1 AND a.state='sending' AND l.sending_fence=l.fence AND l.worker_id IS NOT NULL`, p.i.ActionID).Scan(&sending); err != nil || sending != 1 {
		p.t.Errorf("provider before durable sending: %d %v", sending, err)
	}
}
func (p *recoveredProvider) Complete(_ context.Context, req orchestrator.LLMRequest) (string, error) {
	p.check(req)
	p.calls.Add(1)
	return "recovered response", nil
}
func (p *recoveredProvider) Stream(_ context.Context, req orchestrator.LLMRequest, emit orchestrator.StreamHandler) error {
	p.check(req)
	p.streams.Add(1)
	return emit("recovered response")
}

func recoveryOrchestrator(a *Authority, p *recoveredProvider) *orchestrator.Orchestrator {
	limits := p.i.OperatorCeilings.Clone()
	return orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("not-original", "not-original", p.Name()), map[string]orchestrator.LLMProvider{p.Name(): p}, orchestrator.WithBudgetAuthority(a), orchestrator.WithOperatorCeilings(&limits))
}

func TestPostgresAutomaticPreparedRecoveryAfterProcessDeath(t *testing.T) {
	for _, mode := range []string{"complete", "stream"} {
		for _, stage := range []string{"prepare", "claim", "send"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				a, i, dsn := preparedActionFixture(t)
				i.Invocation = mode
				raw, _ := json.Marshal(i)
				child := exec.Command(os.Args[0], "-test.run=^TestPreparedActionCrashChild$")
				child.Env = append(os.Environ(), "SEVENREVIEW_PREPARED_CRASH_STAGE="+stage, "SEVENREVIEW_PREPARED_CRASH_DSN="+dsn, "SEVENREVIEW_PREPARED_CRASH_INTENT="+string(raw))
				err := child.Run()
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 67 {
					t.Fatalf("child boundary: %v", err)
				}
				if stage == "claim" {
					if _, err = a.db.Exec(`SELECT pg_sleep(greatest(0,extract(epoch FROM (lease_until-clock_timestamp())))+0.002) FROM budget_action_leases WHERE action_id=$1`, i.ActionID); err != nil {
						t.Fatal(err)
					}
				}
				p := &recoveredProvider{t: t, a: a, i: i}
				start := make(chan struct{})
				var wg sync.WaitGroup
				if stage == "claim" {
					old := orchestrator.ActionClaim{ID: i.ActionID, RequestDigest: i.RequestDigest, WorkerID: "dead-worker", Fence: 1, Job: i.Job}
					if err := a.db.QueryRow(`SELECT lease_until FROM budget_action_leases WHERE action_id=$1`, i.ActionID).Scan(&old.LeaseUntil); err != nil {
						t.Fatal(err)
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						if permit, err := a.BeginPrepared(context.Background(), old); err == nil && !permit.AlreadyStarted {
							t.Error("stale original owner obtained sending permission")
						}
					}()
				}
				for n := 0; n < 2; n++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						reopened := ledgerAuthority(t, a.db, ledgerLimits())
						_, _ = recoveryOrchestrator(reopened, p).RecoverPreparedActions(context.Background(), 8)
					}()
				}
				close(start)
				wg.Wait()
				want := int32(1)
				if stage == "send" {
					want = 0
				}
				if p.calls.Load()+p.streams.Load() != want || (mode == "stream" && p.calls.Load() != 0) || (mode == "complete" && p.streams.Load() != 0) {
					t.Fatalf("effects: complete=%d stream=%d want=%d", p.calls.Load(), p.streams.Load(), want)
				}
				o := recoveryOrchestrator(a, p)
				if n, err := o.RecoverPreparedActions(context.Background(), 8); err != nil || n != 0 {
					t.Fatalf("replay: %d %v", n, err)
				}
				if p.calls.Load()+p.streams.Load() != want {
					t.Fatal("replay resent action")
				}
				n, err := a.RecoverActionResults(context.Background(), 8)
				if err != nil || n != int(want) {
					t.Fatalf("journal recovery: %d %v", n, err)
				}
				var actions, holds, settled int
				_ = a.db.QueryRow(`SELECT count(*),count(*) FILTER(WHERE settled) FROM budget_actions`).Scan(&actions, &settled)
				_ = a.db.QueryRow(`SELECT count(*) FROM budget_holds`).Scan(&holds)
				if actions != 1 || holds != 3 || settled != 0 {
					t.Fatalf("recovery reset accounting: %d %d %d", actions, holds, settled)
				}
			})
		}
	}
}

func TestPostgresAutomaticRecoveryInterruptedBeforeAndAfterSending(t *testing.T) {
	for _, stage := range []string{"begin", "outcome"} {
		t.Run(stage, func(t *testing.T) {
			a, i, _ := preparedActionFixture(t)
			i.Invocation = "complete"
			if _, err := a.PrepareAction(context.Background(), i); err != nil {
				t.Fatal(err)
			}
			p := &recoveredProvider{t: t, a: a, i: i}
			o := recoveryOrchestrator(a, p)
			state := "sending"
			if stage == "outcome" {
				state = "result_recorded"
			}
			_, err := a.db.Exec(`CREATE FUNCTION interrupt_recovery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='` + state + `' THEN RAISE EXCEPTION 'injected recovery commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER interrupt_recovery AFTER UPDATE ON budget_actions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION interrupt_recovery()`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = o.RecoverPreparedActions(context.Background(), 8); err == nil {
				t.Fatal("interrupted recovery reported success")
			}
			want := int32(0)
			if stage == "outcome" {
				want = 1
			}
			if p.calls.Load() != want {
				t.Fatalf("calls before retry: %d", p.calls.Load())
			}
			if _, err = a.db.Exec(`DROP TRIGGER interrupt_recovery ON budget_actions; UPDATE budget_action_leases SET lease_until=clock_timestamp()-interval '1 second'`); err != nil {
				t.Fatal(err)
			}
			n, err := o.RecoverPreparedActions(context.Background(), 8)
			if err != nil || (stage == "begin" && n != 1) || (stage == "outcome" && n != 0) || p.calls.Load() != 1 {
				t.Fatalf("interrupted replay: n=%d calls=%d err=%v", n, p.calls.Load(), err)
			}
		})
	}
}
