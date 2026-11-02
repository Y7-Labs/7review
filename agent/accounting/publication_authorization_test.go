package accounting

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func publicationAuthorizationFixture(t *testing.T) (*Authority, orchestrator.DispatchIntent, review.OperatorActor, review.OperatorActor, review.AuthorizePublicationCommand, string) {
	t.Helper()
	a, intent, _, dsn := recordedReductionFixtureDSN(t)
	ctx := context.Background()
	if _, err := a.RecoverModelReductions(ctx, 128, pipeline.ModelReducerV1{}); err != nil {
		t.Fatal(err)
	}
	projection, err := a.CompleteModelJob(ctx, intent.ActionID)
	if err != nil {
		t.Fatal(err)
	}
	var assessmentDigest string
	if err = a.db.QueryRow(`SELECT m.artifact_digest FROM budget_jobs j JOIN budget_model_reductions m ON m.id=j.terminal_reduction_id WHERE j.run_id=$1`, intent.Job.RunID).Scan(&assessmentDigest); err != nil {
		t.Fatal(err)
	}
	change := intent.Attempt.Change
	destination := review.PublicationDestination{Provider: change.Provider, Host: change.Host, RepositoryID: change.RepositoryID, ChangeNumber: change.ChangeNumber, ArtifactClass: "assessment"}
	command := review.AuthorizePublicationCommand{SchemaVersion: 1, CommandID: "publish-1", AttemptID: intent.Attempt.AttemptID, ExpectedVersion: 2, Revision: intent.Attempt.Snapshot.AnalysisRevision(), Kind: "authorize_publication", Payload: review.AuthorizePublicationPayload{AssessmentDigest: assessmentDigest, ArtifactDigest: hash([]byte(projection.Source.Report.Draft)), Destination: destination, Reason: "reviewed exact assessment"}}
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

func TestPublicationAuthorizationPersistsExactTerminalArtifactAndReplaysReceipt(t *testing.T) {
	a, intent, actor, _, command, dsn := publicationAuthorizationFixture(t)
	ctx := context.Background()
	receipt, err := a.AuthorizePublication(ctx, actor, command)
	if err != nil || receipt == nil || receipt.State != "authorized" || receipt.ControlVersion != 3 || receipt.AssessmentDigest != command.Payload.AssessmentDigest || receipt.ArtifactDigest != command.Payload.ArtifactDigest || receipt.Destination != command.Payload.Destination {
		t.Fatalf("authorization: %+v %v", receipt, err)
	}
	var commands, version int
	var pointer string
	if err = a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),control_version,publication_authorization_id FROM budget_jobs WHERE run_id=$1`, intent.Job.RunID).Scan(&commands, &version, &pointer); err != nil || commands != 1 || version != 3 || pointer == "" {
		t.Fatalf("durable authorization: %d %d %s %v", commands, version, pointer, err)
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
	replayed, err := restarted.AuthorizePublication(ctx, actor, command)
	if err != nil || !reflect.DeepEqual(receipt, replayed) {
		t.Fatalf("replay: %+v %v", replayed, err)
	}
	later := command
	later.CommandID = "publish-2"
	later.ExpectedVersion = 3
	later.Payload.Reason = "second exact authorization"
	if next, err := restarted.AuthorizePublication(ctx, actor, later); err != nil || next.ControlVersion != 4 {
		t.Fatalf("later authorization: %+v %v", next, err)
	}
	replayed, err = restarted.AuthorizePublication(ctx, actor, command)
	if err != nil || !reflect.DeepEqual(receipt, replayed) {
		t.Fatalf("older exact replay after later decision: %+v %v", replayed, err)
	}
	changed := command
	changed.Payload.Reason = "different decision"
	if r, err := restarted.AuthorizePublication(ctx, actor, changed); !errors.Is(err, ErrOperatorCommandConflict) || r != nil {
		t.Fatalf("command ID payload collision accepted: %+v %v", r, err)
	}
}

func TestPublicationAuthorizationRejectsWrongRoleDestinationAndArtifact(t *testing.T) {
	for _, fault := range []string{"role", "destination", "assessment", "artifact", "version", "revision"} {
		t.Run(fault, func(t *testing.T) {
			a, _, actor, participant, command, _ := publicationAuthorizationFixture(t)
			switch fault {
			case "role":
				actor = participant
			case "destination":
				command.Payload.Destination.RepositoryID = "other/repo"
			case "assessment":
				command.Payload.AssessmentDigest = "sha256:" + strings.Repeat("a", 64)
			case "artifact":
				command.Payload.ArtifactDigest = "sha256:" + strings.Repeat("b", 64)
			case "version":
				command.ExpectedVersion++
			case "revision":
				command.Revision = "other-head"
			}
			if r, err := a.AuthorizePublication(context.Background(), actor, command); err == nil || r != nil {
				t.Fatalf("invalid authority accepted: %+v %v", r, err)
			}
			var commands int
			if err := a.db.QueryRow(`SELECT count(*) FROM budget_operator_commands`).Scan(&commands); err != nil || commands != 0 {
				t.Fatalf("rejected command persisted: %d %v", commands, err)
			}
		})
	}
}

func TestPublicationAuthorizationCommitFailureCannotPartiallyAuthorize(t *testing.T) {
	a, intent, actor, _, command, _ := publicationAuthorizationFixture(t)
	if _, err := a.db.Exec(`CREATE FUNCTION fail_publication_authorization() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected publication authorization failure'; END $$; CREATE CONSTRAINT TRIGGER fail_publication_authorization AFTER INSERT ON budget_operator_commands DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_publication_authorization()`); err != nil {
		t.Fatal(err)
	}
	if r, err := a.AuthorizePublication(context.Background(), actor, command); err == nil || r != nil {
		t.Fatal("failed authorization acknowledged")
	}
	var commands, version int
	var pointer bool
	if err := a.db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),control_version,publication_authorization_id IS NULL FROM budget_jobs WHERE run_id=$1`, intent.Job.RunID).Scan(&commands, &version, &pointer); err != nil || commands != 0 || version != 2 || !pointer {
		t.Fatalf("partial authorization: %d %d %v %v", commands, version, pointer, err)
	}
}
