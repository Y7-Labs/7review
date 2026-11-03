package accounting

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func findingDispositionFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent, review.OperatorActor, review.OperatorActor, review.FindingDispositionCommand, string) {
	t.Helper()
	response := []byte(`[{"id":"F-1","severity":"high","title":"Authorization bypass","description":"The guard is skipped.","suggestion":"Enforce the guard.","location":{"path":"service.go","line":1},"confidence":0.91}]`)
	a, intent, _, dsn := recordedReductionFixtureResponse(t, response)
	ctx := context.Background()
	if _, err := a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
		t.Fatal(err)
	}
	projection, err := a.CompleteModelJob(ctx, intent.ActionID)
	if err != nil || len(projection.Source.Findings) != 1 {
		t.Fatalf("terminal finding fixture: %#v %v", projection, err)
	}
	finding := projection.Source.Findings[0]
	digest, err := review.CanonicalFindingDigest(finding)
	if err != nil {
		t.Fatal(err)
	}
	command := review.FindingDispositionCommand{SchemaVersion: 1, CommandID: "finding-1", AttemptID: intent.Attempt.AttemptID, ExpectedVersion: 2, Revision: intent.Attempt.Snapshot.AnalysisRevision(), Kind: "disposition_finding", Payload: review.FindingDispositionPayload{FindingID: finding.ID, FindingDigest: digest, Decision: review.FindingDisputed, Reason: "the cited branch is unreachable"}}
	change := intent.Attempt.Change
	raw := `{"schema_version":1,"principals":[{"id":"alice","token_env":"ALICE_TOKEN","grants":[{"boundary_id":"` + a.cfg.BoundaryID + `","project_id":"` + a.cfg.ProjectID + `","provider":"` + change.Provider + `","host":"` + change.Host + `","repository_id":"` + change.RepositoryID + `","roles":["reviewer"]}]},{"id":"bob","token_env":"BOB_TOKEN","grants":[{"boundary_id":"` + a.cfg.BoundaryID + `","project_id":"` + a.cfg.ProjectID + `","provider":"` + change.Provider + `","host":"` + change.Host + `","repository_id":"` + change.RepositoryID + `","roles":["participant"]}]}]}`
	directory, err := review.DecodeOperatorDirectory([]byte(raw), func(name string) string { return strings.ToLower(strings.TrimSuffix(name, "_TOKEN")) + "-secret" })
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Operators = directory
	alice, err := directory.Authenticate("alice-secret")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := directory.Authenticate("bob-secret")
	if err != nil {
		t.Fatal(err)
	}
	return a, intent, alice, bob, command, dsn
}

func TestFindingDispositionPersistsExactTerminalSubjectAndReplaysAfterReplacement(t *testing.T) {
	a, intent, actor, _, command, dsn := findingDispositionFixture(t)
	ctx := context.Background()
	receipt, err := a.DispositionFinding(ctx, actor, command)
	if err != nil || receipt == nil || receipt.State != "recorded" || receipt.ControlVersion != 3 || receipt.FindingDigest != command.Payload.FindingDigest || receipt.Decision != review.FindingDisputed {
		t.Fatalf("finding disposition: %#v %v", receipt, err)
	}
	var commands, dispositions, version int
	var head string
	if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),(SELECT count(*) FROM budget_finding_dispositions),control_version,(SELECT disposition_id FROM budget_finding_disposition_heads WHERE attempt_id=$2 AND finding_id=$3) FROM budget_jobs WHERE run_id=$1`, intent.Job.RunID, command.AttemptID, command.Payload.FindingID).Scan(&commands, &dispositions, &version, &head); err != nil || commands != 1 || dispositions != 1 || version != 3 || head == "" {
		t.Fatalf("durable finding disposition: %d %d %d %q %v", commands, dispositions, version, head, err)
	}
	if _, err = a.db.Exec(`INSERT INTO budget_finding_disposition_heads(boundary_id,project_id,attempt_id,finding_id,disposition_id) VALUES($1,$2,$3,'F-forged',$4)`, a.cfg.BoundaryID, a.cfg.ProjectID, command.AttemptID, head); err == nil {
		t.Fatal("current head accepted a disposition bound to another finding")
	}
	restartedDB, err := OpenDatabase(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer restartedDB.Close()
	restarted, err := NewAuthority(restartedDB, a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.DispositionFinding(ctx, actor, command)
	if err != nil || !reflect.DeepEqual(receipt, replayed) {
		t.Fatalf("restart replay: %#v %v", replayed, err)
	}
	later := command
	later.CommandID = "finding-2"
	later.ExpectedVersion = 3
	later.Payload.Decision = review.FindingRejected
	later.Payload.Reason = "not actionable in this change"
	next, err := restarted.DispositionFinding(ctx, actor, later)
	if err != nil || next.ControlVersion != 4 || next.Decision != review.FindingRejected {
		t.Fatalf("replacement disposition: %#v %v", next, err)
	}
	if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_finding_dispositions),(SELECT d.command_id FROM budget_finding_disposition_heads h JOIN budget_operator_commands d ON d.id=h.disposition_id WHERE h.attempt_id=$1 AND h.finding_id=$2)`, command.AttemptID, command.Payload.FindingID).Scan(&dispositions, &head); err != nil || dispositions != 2 || head != later.CommandID {
		t.Fatalf("replacement lost history/head: %d %q %v", dispositions, head, err)
	}
	replayed, err = restarted.DispositionFinding(ctx, actor, command)
	if err != nil || !reflect.DeepEqual(receipt, replayed) {
		t.Fatalf("old exact replay after replacement: %#v %v", replayed, err)
	}
	changed := command
	changed.Payload.Reason = "different reason"
	if got, err := restarted.DispositionFinding(ctx, actor, changed); !errors.Is(err, ErrOperatorCommandConflict) || got != nil {
		t.Fatalf("command collision accepted: %#v %v", got, err)
	}
}

func TestFindingDispositionRejectsRoleSubjectVersionAndRevisionGaps(t *testing.T) {
	for _, fault := range []string{"role", "missing", "digest", "version", "revision"} {
		t.Run(fault, func(t *testing.T) {
			a, _, reviewer, participant, command, _ := findingDispositionFixture(t)
			actor := reviewer
			switch fault {
			case "role":
				actor = participant
				command.Payload.Decision = review.FindingAccepted
			case "missing":
				command.Payload.FindingID = "F-404"
			case "digest":
				command.Payload.FindingDigest = "sha256:" + strings.Repeat("f", 64)
			case "version":
				command.ExpectedVersion++
			case "revision":
				command.Revision = "other-head"
			}
			if receipt, err := a.DispositionFinding(context.Background(), actor, command); err == nil || receipt != nil {
				t.Fatalf("invalid disposition accepted: %#v %v", receipt, err)
			}
			var commands, dispositions int
			if err := a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),(SELECT count(*) FROM budget_finding_dispositions)`).Scan(&commands, &dispositions); err != nil || commands != 0 || dispositions != 0 {
				t.Fatalf("rejected disposition persisted: %d %d %v", commands, dispositions, err)
			}
		})
	}
	a, _, _, participant, command, _ := findingDispositionFixture(t)
	if receipt, err := a.DispositionFinding(context.Background(), participant, command); err != nil || receipt == nil || receipt.ActorID != "bob" || receipt.Decision != review.FindingDisputed {
		t.Fatalf("repository participant could not dispute exact finding: %#v %v", receipt, err)
	}
}

func TestFindingDispositionCommitFailureAndConcurrentVersionRaceAreAtomic(t *testing.T) {
	a, intent, actor, _, command, _ := findingDispositionFixture(t)
	if _, err := a.db.Exec(`CREATE FUNCTION fail_finding_disposition() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected finding disposition failure'; END $$; CREATE CONSTRAINT TRIGGER fail_finding_disposition AFTER INSERT ON budget_finding_dispositions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_finding_disposition()`); err != nil {
		t.Fatal(err)
	}
	if receipt, err := a.DispositionFinding(context.Background(), actor, command); err == nil || receipt != nil {
		t.Fatal("failed disposition acknowledged")
	}
	var commands, dispositions, heads, version int
	if err := a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),(SELECT count(*) FROM budget_finding_dispositions),(SELECT count(*) FROM budget_finding_disposition_heads),control_version FROM budget_jobs WHERE run_id=$1`, intent.Job.RunID).Scan(&commands, &dispositions, &heads, &version); err != nil || commands != 0 || dispositions != 0 || heads != 0 || version != 2 {
		t.Fatalf("partial finding disposition: %d %d %d %d %v", commands, dispositions, heads, version, err)
	}
	if _, err := a.db.Exec(`DROP TRIGGER fail_finding_disposition ON budget_finding_dispositions`); err != nil {
		t.Fatal(err)
	}
	other := command
	other.CommandID = "finding-concurrent"
	other.Payload.Decision = review.FindingAccepted
	other.Payload.Reason = "confirmed"
	start := make(chan struct{})
	results := make(chan *review.FindingDispositionReceipt, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, candidate := range []review.FindingDispositionCommand{command, other} {
		wg.Add(1)
		go func(c review.FindingDispositionCommand) {
			defer wg.Done()
			<-start
			r, err := a.DispositionFinding(context.Background(), actor, c)
			results <- r
			errs <- err
		}(candidate)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	successes, conflicts := 0, 0
	for result := range results {
		if result != nil {
			successes++
		}
	}
	for err := range errs {
		if errors.Is(err, ErrOperatorCommandConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent dispositions did not serialize: successes=%d conflicts=%d", successes, conflicts)
	}
}
