package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func toolChildParentFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent) {
	t.Helper()
	a, i, _ := toolChildParentFixtureDSN(t, ledgerLimits())
	return a, i
}

func toolChildParentFixtureDSN(t *testing.T, limits review.ResourceLimits) (*Authority, orchestrator.DispatchIntent, string) {
	t.Helper()
	a, i, dsn := preparedActionFixtureWithLimits(t, limits)
	ctx := context.Background()
	aggregate, err := a.LoadAggregate(ctx, *i.Job)
	if err != nil {
		t.Fatal(err)
	}
	aggregate.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "service.go", Patch: "+change"}}}
	routing := &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{"reasoner": {Routes: []review.ModelRoute{{Provider: i.Provider, Model: i.Model}}, MaxTokens: 20}, "formatter": {Routes: []review.ModelRoute{{Provider: i.Provider, Model: i.Model}}, MaxTokens: 20}}}
	cpctx, err := review.WithModelCheckpoint(review.WithModelRouting(review.WithModelValidation(ctx, &review.ModelValidation{Version: 1, MinConfidence: 0.45}), routing), &review.Context{Source: aggregate.Source}, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	i.Invocation, i.Step, i.ModelCheckpoint = "complete", "step5", review.ModelCheckpointFromContext(cpctx)
	p, err := a.PrepareAction(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	recordToolChildResponse(t, a, p, `{"tool_requests":[{"name":"get_changed_files"}]}`)
	if _, err := a.CommitModelToolObservations(ctx, i.ActionID, pipeline.ModelToolObserverV1{}); err != nil {
		t.Fatal(err)
	}
	return a, i, dsn
}

func recordToolChildResponse(t *testing.T, a *Authority, p orchestrator.PreparedAction, response string) {
	t.Helper()
	ctx := context.Background()
	c, err := a.ReclaimPrepared(ctx, p, "tool-child-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.BeginPrepared(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(response)
	if err := a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: raw, ResponseDigest: hash(raw), ResponseReceived: true, ElapsedMS: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecoverActionResults(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if n, err := a.RecoverModelReductions(ctx, 8, pipeline.ModelReducerV1{}); err != nil || n != 1 {
		t.Fatalf("reduction: %d %v", n, err)
	}
}

func TestPostgresToolChildCreationIsAtomicIdempotentAndConfined(t *testing.T) {
	a, i := toolChildParentFixture(t)
	ctx := context.Background()
	var before string
	if err := a.db.QueryRow(`SELECT claim_token FROM budget_inbox`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`CREATE FUNCTION fail_tool_child_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected tool child commit failure'; END $$; CREATE CONSTRAINT TRIGGER fail_tool_child_commit AFTER INSERT ON budget_model_tool_children DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_tool_child_commit()`); err != nil {
		t.Fatal(err)
	}
	if p, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{}); err == nil || p.ID != "" {
		t.Fatalf("uncommitted child escaped: %#v %v", p, err)
	}
	var token string
	var actions, holds, links int
	if err := a.db.QueryRow(`SELECT claim_token,(SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_model_tool_children) FROM budget_inbox`).Scan(&token, &actions, &holds, &links); err != nil || token != before || actions != 1 || holds != 3 || links != 0 {
		t.Fatalf("failed child changed authority/accounting: %s %d %d %d %v", token, actions, holds, links, err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_tool_child_commit ON budget_model_tool_children`); err != nil {
		t.Fatal(err)
	}
	results := make(chan orchestrator.PreparedAction, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{})
			results <- p
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for p := range results {
		if p.ID == "" || (id != "" && id != p.ID) {
			t.Fatal("duplicate child identity")
		}
		id = p.ID
	}
	var cursor string
	if err := a.db.QueryRow(`SELECT i.claim_token,j.recovery_action_id,(SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_model_tool_children) FROM budget_inbox i JOIN budget_jobs j USING(run_id)`).Scan(&token, &cursor, &actions, &holds, &links); err != nil || token == before || cursor != id || actions != 2 || holds != 6 || links != 1 {
		t.Fatalf("child was not atomically reserved/confined: %s %s %d %d %d %v", token, cursor, actions, holds, links, err)
	}
	if err := a.FinishInbox(ctx, i.Job.RunID, token, true); err == nil {
		t.Fatal("prepared tool child gained whole-job completion rights")
	}
	if _, err := a.db.Exec(`UPDATE budget_attempts SET active=false`); err != nil {
		t.Fatal(err)
	}
	if p, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{}); err != nil || p.ID != id {
		t.Fatalf("historical child receipt disappeared: %#v %v", p, err)
	}
}

func TestPostgresToolChildrenCarryAllRoundsThroughCheckedTerminalCompletion(t *testing.T) {
	a, i := toolChildParentFixture(t)
	ctx := context.Background()
	parentID := i.ActionID
	for round := 1; round <= review.MaxModelToolRounds; round++ {
		p, err := a.AdvanceModelToolContinuation(ctx, parentID, pipeline.ModelToolContinuationV1{})
		if err != nil {
			t.Fatalf("advance round %d: %v", round, err)
		}
		tx, err := a.begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		intent, _, err := a.storedAction(ctx, tx, p.ID)
		tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		cp, err := review.DecodeModelCheckpoint(intent.ModelCheckpoint)
		if err != nil {
			t.Fatal(err)
		}
		if cp.Cursor.Round != round || len(cp.Source.ToolRequests) != round || len(cp.Source.ToolObservations) != round || len(cp.RawResponses) != round {
			t.Fatalf("round %d duplicated or lost evidence: %#v", round, cp)
		}
		response := `{"tool_requests":[{"name":"get_changed_files"}]}`
		if round == review.MaxModelToolRounds {
			response = `[]`
		}
		recordToolChildResponse(t, a, p, response)
		parentID = p.ID
		if round < review.MaxModelToolRounds {
			if _, err := a.CommitModelToolObservations(ctx, p.ID, pipeline.ModelToolObserverV1{}); err != nil {
				t.Fatalf("observe round %d: %v", round+1, err)
			}
		}
	}
	projection, err := a.CompleteModelJob(ctx, parentID)
	if err != nil || !projection.Succeeded || projection.Source.Report.Draft == "" || projection.Source.Report.Final != "" || len(projection.Source.ToolObservations) != 3 {
		t.Fatalf("tool chain could not finish locally: %#v %v", projection, err)
	}
	var actions, holds, observations, children, settled int
	if err := a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_model_tool_children),(SELECT count(*) FROM budget_actions WHERE settled)`).Scan(&actions, &holds, &observations, &children, &settled); err != nil || actions != 4 || holds != 12 || observations != 3 || children != 3 || settled != 0 {
		t.Fatalf("tool chain reset or duplicated spend: %d %d %d %d %d %v", actions, holds, observations, children, settled, err)
	}
}

type faultyToolBuilder struct{ fault string }

func (b faultyToolBuilder) BuildModelToolContinuation(parent review.ModelReduction, observations review.ModelToolObservations) (orchestrator.ModelContinuationPlan, error) {
	if b.fault == "panic" {
		panic("interrupted tool continuation builder")
	}
	p, err := (pipeline.ModelToolContinuationV1{}).BuildModelToolContinuation(parent, observations)
	if err != nil {
		return p, err
	}
	var cp review.ModelCheckpoint
	if err = json.Unmarshal(p.Intent.ModelCheckpoint, &cp); err != nil {
		return p, err
	}
	switch b.fault {
	case "round":
		cp.Cursor.Round++
	case "observations":
		cp.Source.ToolObservations = nil
	case "role":
		p.Intent.Role = "formatter"
	case "route":
		p.Intent.Model = "different"
	case "native_tools", "max_tokens":
		var req orchestrator.LLMRequest
		if err = json.Unmarshal(p.Intent.Payload, &req); err != nil {
			return p, err
		}
		if b.fault == "native_tools" {
			req.Tools = append(req.Tools, orchestrator.ToolDefinition{Name: "shell"})
		} else {
			req.MaxTokens++
		}
		p.Intent.Payload, _ = json.Marshal(req)
		p.Intent.RequestDigest = hash(p.Intent.Payload)
	}
	p.Intent.ModelCheckpoint, _ = json.Marshal(cp)
	return p, nil
}

func TestPostgresToolChildRejectsUnboundEvidenceAndChangedAuthority(t *testing.T) {
	for _, fault := range []string{"round", "observations", "role", "route", "native_tools", "max_tokens", "panic", "epoch", "pointer", "head", "deadline", "receipt"} {
		t.Run(fault, func(t *testing.T) {
			a, i := toolChildParentFixture(t)
			var builder orchestrator.ModelToolContinuationBuilder = pipeline.ModelToolContinuationV1{}
			query := ""
			switch fault {
			case "round", "observations", "role", "route", "native_tools", "max_tokens", "panic":
				builder = faultyToolBuilder{fault}
			case "epoch":
				query = `UPDATE budget_inbox SET claim_token='other-owner'`
			case "pointer":
				query = `UPDATE budget_jobs SET tool_observation_id=NULL`
			case "head":
				query = `UPDATE budget_heads SET current_attempt_id=NULL`
			case "deadline":
				query = `UPDATE budget_inbox SET deadline=clock_timestamp()`
			case "receipt":
				query = `UPDATE budget_model_tool_observations SET receipt_digest='changed'`
			}
			if query != "" {
				if _, err := a.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			var before string
			if err := a.db.QueryRow(`SELECT claim_token FROM budget_inbox`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if p, err := a.AdvanceModelToolContinuation(context.Background(), i.ActionID, builder); err == nil || p.ID != "" {
				t.Fatalf("%s granted a child: %#v %v", fault, p, err)
			}
			var n int
			if err := a.db.QueryRow(`SELECT count(*) FROM budget_actions`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("denied child left a reservation: %d %v", n, err)
			}
			var after string
			if err := a.db.QueryRow(`SELECT claim_token FROM budget_inbox`).Scan(&after); err != nil || after != before {
				t.Fatalf("denied child changed the owner epoch: %s %s %v", before, after, err)
			}
		})
	}
}

func TestPostgresToolChildBudgetDenialPreservesAcceptedObservationAndEpoch(t *testing.T) {
	for _, kind := range []string{"attempt", "change", "project"} {
		t.Run(kind, func(t *testing.T) {
			limits := ledgerLimits()
			scopes := map[string]*review.ResourceLimitScope{"attempt": &limits.Attempt, "change": &limits.Change, "project": &limits.Project}
			scopes[kind].ModelCalls = 1
			a, i, _ := toolChildParentFixtureDSN(t, limits)
			var before string
			if err := a.db.QueryRow(`SELECT claim_token FROM budget_inbox`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if p, err := a.AdvanceModelToolContinuation(context.Background(), i.ActionID, pipeline.ModelToolContinuationV1{}); err == nil || p.ID != "" {
				t.Fatalf("%s model ceiling bypassed: %#v %v", kind, p, err)
			}
			var token string
			var actions, holds, observations, links int
			if err := a.db.QueryRow(`SELECT claim_token,(SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_model_tool_observations),(SELECT count(*) FROM budget_model_tool_children) FROM budget_inbox`).Scan(&token, &actions, &holds, &observations, &links); err != nil || token != before || actions != 1 || holds != 3 || observations != 1 || links != 0 {
				t.Fatalf("denied child erased accepted spend or moved ownership: %s %d %d %d %d %v", token, actions, holds, observations, links, err)
			}
		})
	}
}

func TestPostgresToolChildRecoveryNeverReplaysRecordedParentOrSendingChild(t *testing.T) {
	for _, stage := range []string{"prepare", "claim", "send"} {
		t.Run(stage, func(t *testing.T) {
			a, i, dsn := toolChildParentFixtureDSN(t, ledgerLimits())
			ctx := context.Background()
			p, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := a.begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			intent, _, err := a.storedAction(ctx, tx, p.ID)
			tx.Rollback()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(intent)
			child := exec.Command(os.Args[0], "-test.run=^TestPreparedActionCrashChild$")
			child.Env = append(os.Environ(), "SEVENREVIEW_PREPARED_CRASH_STAGE="+stage, "SEVENREVIEW_PREPARED_CRASH_DSN="+dsn, "SEVENREVIEW_PREPARED_CRASH_INTENT="+string(raw))
			if output, err := child.CombinedOutput(); err == nil {
				t.Fatal("child did not exit")
			} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 67 {
				t.Fatalf("crash boundary: %v %s", err, output)
			}
			if stage == "claim" {
				if _, err := a.db.Exec(`SELECT pg_sleep(greatest(0,extract(epoch FROM (lease_until-clock_timestamp())))+0.002) FROM budget_action_leases WHERE action_id=$1`, p.ID); err != nil {
					t.Fatal(err)
				}
			}
			provider := &continuedFormatterProvider{t: t, a: a, intent: intent}
			limits := ledgerLimits()
			o := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("changed-current-model", "changed-current-formatter", provider.Name()), map[string]orchestrator.LLMProvider{provider.Name(): provider}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(a))
			want := 1
			if stage == "send" {
				want = 0
			}
			if n, err := o.RecoverPreparedActions(ctx, 8); err != nil || n != want || provider.calls.Load() != int32(want) {
				t.Fatalf("tool child recovery: n=%d calls=%d %v", n, provider.calls.Load(), err)
			}
			if n, err := o.RecoverPreparedActions(ctx, 8); err != nil || n != 0 || provider.calls.Load() != int32(want) {
				t.Fatalf("tool child resend: %d %v", n, err)
			}
			if want == 1 {
				if _, err := a.RecoverActionResults(ctx, 8); err != nil {
					t.Fatal(err)
				}
				if _, err := a.RecoverModelReductions(ctx, 8, pipeline.ModelReducerV1{}); err != nil {
					t.Fatal(err)
				}
				if projection, err := a.CompleteModelJob(ctx, p.ID); err != nil || !projection.Succeeded {
					t.Fatalf("recovered child could not finish locally: %#v %v", projection, err)
				}
			}
			if replay, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{}); err != nil || replay.ID != p.ID || !replay.AlreadyStarted {
				t.Fatalf("parent replay created new spending: %#v %v", replay, err)
			}
		})
	}
}

func TestPostgresToolChildBeginRechecksObservationAndParentAfterClaim(t *testing.T) {
	for _, fault := range []string{"receipt", "pointer", "outcome"} {
		t.Run(fault, func(t *testing.T) {
			a, i := toolChildParentFixture(t)
			ctx := context.Background()
			p, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := a.ReclaimPrepared(ctx, p, "claimed-tool-child", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			query := `UPDATE budget_model_tool_observations SET receipt_digest='changed'`
			if fault == "pointer" {
				query = `UPDATE budget_jobs SET tool_observation_id=NULL`
			}
			if fault == "outcome" {
				query = `UPDATE budget_actions SET outcome=convert_to('{}','UTF8') WHERE state='result_recorded'`
			}
			if _, err := a.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if permit, err := a.BeginPrepared(ctx, claim); err == nil || permit.ID != "" {
				t.Fatalf("%s changed after claim but still sent: %#v %v", fault, permit, err)
			}
			var state string
			if err := a.db.QueryRow(`SELECT state FROM budget_actions WHERE id=$1`, p.ID).Scan(&state); err != nil || state != "prepared" {
				t.Fatalf("invalid child crossed sending boundary: %s %v", state, err)
			}
		})
	}
}

func TestToolChildAdvanceCrashHelper(t *testing.T) {
	dsn := os.Getenv("SEVENREVIEW_TOOL_CHILD_CRASH_DSN")
	if dsn == "" {
		t.Skip("subprocess helper")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := ledgerAuthority(t, db, ledgerLimits())
	if _, err := a.AdvanceModelToolContinuation(context.Background(), os.Getenv("SEVENREVIEW_TOOL_CHILD_CRASH_PARENT"), pipeline.ModelToolContinuationV1{}); err != nil {
		t.Fatal(err)
	}
	os.Exit(72)
}

func TestPostgresToolChildLostCommitAcknowledgementDoesNotReserveAgain(t *testing.T) {
	a, i, dsn := toolChildParentFixtureDSN(t, ledgerLimits())
	var token string
	for n := 0; n < 2; n++ {
		child := exec.Command(os.Args[0], "-test.run=^TestToolChildAdvanceCrashHelper$")
		child.Env = append(os.Environ(), "SEVENREVIEW_TOOL_CHILD_CRASH_DSN="+dsn, "SEVENREVIEW_TOOL_CHILD_CRASH_PARENT="+i.ActionID)
		if output, err := child.CombinedOutput(); err == nil {
			t.Fatal("child did not exit")
		} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 72 {
			t.Fatalf("child commit boundary: %v %s", err, output)
		}
		var current string
		var actions, holds, links int
		if err := a.db.QueryRow(`SELECT claim_token,(SELECT count(*) FROM budget_actions),(SELECT count(*) FROM budget_holds),(SELECT count(*) FROM budget_model_tool_children) FROM budget_inbox`).Scan(&current, &actions, &holds, &links); err != nil || actions != 2 || holds != 6 || links != 1 {
			t.Fatalf("lost response duplicated model reservation: %s %d %d %d %v", current, actions, holds, links, err)
		}
		if n == 1 && current != token {
			t.Fatal("replay reacquired ownership")
		}
		token = current
	}
}

func TestPostgresFormatterAfterToolRoundRequiresCompleteObservationAncestry(t *testing.T) {
	for _, stage := range []string{"valid", "before_prepare", "after_claim", "outcome_before_prepare", "outcome_after_claim"} {
		t.Run(stage, func(t *testing.T) {
			a, i := toolChildParentFixture(t)
			ctx := context.Background()
			tool, err := a.AdvanceModelToolContinuation(ctx, i.ActionID, pipeline.ModelToolContinuationV1{})
			if err != nil {
				t.Fatal(err)
			}
			recordToolChildResponse(t, a, tool, "malformed reasoner output")
			query := `UPDATE budget_model_tool_observations SET receipt_digest='changed'`
			if stage == "outcome_before_prepare" || stage == "outcome_after_claim" {
				query = `UPDATE budget_actions SET outcome=convert_to('{}','UTF8') WHERE id=$1`
			}
			before := stage == "before_prepare" || stage == "outcome_before_prepare"
			tamper := func() {
				var err error
				if stage == "outcome_before_prepare" || stage == "outcome_after_claim" {
					_, err = a.db.Exec(query, i.ActionID)
				} else {
					_, err = a.db.Exec(query)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if before {
				tamper()
			}
			formatter, err := a.AdvanceModelContinuation(ctx, tool.ID, pipeline.ModelContinuationV1{})
			if before {
				if err == nil || formatter.ID != "" {
					t.Fatalf("formatter reserved from corrupt tool ancestry: %#v %v", formatter, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stage == "valid" {
				recordToolChildResponse(t, a, formatter, `[]`)
				if projection, err := a.CompleteModelJob(ctx, formatter.ID); err != nil || !projection.Succeeded {
					t.Fatalf("mixed tool/formatter chain failed: %#v %v", projection, err)
				}
				return
			}
			claim, err := a.ReclaimPrepared(ctx, formatter, "formatter-after-tool", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			tamper()
			if permit, err := a.BeginPrepared(ctx, claim); err == nil || permit.ID != "" {
				t.Fatalf("formatter sent after observation ancestry changed: %#v %v", permit, err)
			}
		})
	}
}

func TestPostgresSealedBatchCanContinueThroughToolChildWithoutWideningMembership(t *testing.T) {
	a, intents := modelBatchFixture(t)
	ctx := context.Background()
	for n := range intents {
		cp, err := review.DecodeModelCheckpoint(intents[n].ModelCheckpoint)
		if err != nil {
			t.Fatal(err)
		}
		cp.Routing = &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{"reasoner": {Routes: []review.ModelRoute{{Provider: intents[n].Provider, Model: intents[n].Model}}, MaxTokens: 20}}}
		intents[n].ModelCheckpoint, _ = json.Marshal(cp)
	}
	batch, err := a.PrepareModelBatch(ctx, intents)
	if err != nil {
		t.Fatal(err)
	}
	for n, p := range batch {
		claim, err := a.ReclaimPrepared(ctx, p, "sealed-tool-parent", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		permit, err := a.BeginPrepared(ctx, claim)
		if err != nil {
			t.Fatal(err)
		}
		response := []byte(`[]`)
		if n == 0 {
			response = []byte(`{"tool_requests":[{"name":"get_changed_files"}]}`)
		}
		if err := a.RecordOutcome(ctx, permit, orchestrator.DispatchOutcome{Response: response, ResponseDigest: hash(response), ResponseReceived: true, ElapsedMS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.RecoverActionResults(ctx, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RecoverModelReductions(ctx, 8, pipeline.ModelReducerV1{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CommitModelToolObservations(ctx, batch[0].ID, pipeline.ModelToolObserverV1{}); err != nil {
		t.Fatal(err)
	}
	child, err := a.AdvanceModelToolContinuation(ctx, batch[0].ID, pipeline.ModelToolContinuationV1{})
	if err != nil {
		t.Fatal(err)
	}
	recordToolChildResponse(t, a, child, `[]`)
	if projection, err := a.CompleteModelJob(ctx, child.ID); err != nil || !projection.Succeeded {
		t.Fatalf("sealed tool child could not finish: %#v %v", projection, err)
	}
}
