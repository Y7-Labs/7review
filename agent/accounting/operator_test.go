package accounting

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/review"
)

func TestOperatorAttemptRequiresAuthenticatedRepositoryGrant(t *testing.T) {
	a, i, _ := preparedActionFixture(t)
	raw := `{"schema_version":1,"principals":[{"id":"alice","token_env":"ALICE_TOKEN","grants":[{"boundary_id":"` + a.cfg.BoundaryID + `","project_id":"` + a.cfg.ProjectID + `","provider":"` + i.Attempt.Change.Provider + `","host":"` + i.Attempt.Change.Host + `","repository_id":"` + i.Attempt.Change.RepositoryID + `","roles":["operator"]}]}]}`
	d, err := review.DecodeOperatorDirectory([]byte(raw), func(string) string { return "alice-secret" })
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Operators = d
	actor, _ := d.Authenticate("alice-secret")
	v, err := a.OperatorAttempt(context.Background(), actor, i.Attempt.AttemptID)
	if err != nil || v.AttemptID != i.Attempt.AttemptID || v.Revision != i.Attempt.Snapshot.AnalysisRevision() || !v.Current {
		t.Fatalf("view: %+v %v", v, err)
	}
	if _, err = a.OperatorAttempt(context.Background(), review.OperatorActor{}, i.Attempt.AttemptID); !errors.Is(err, review.ErrOperatorUnauthorized) {
		t.Fatalf("zero actor: %v", err)
	}
	other, _ := review.DecodeOperatorDirectory([]byte(raw), func(string) string { return "alice-secret" })
	foreign, _ := other.Authenticate("alice-secret")
	if _, err = a.OperatorAttempt(context.Background(), foreign, i.Attempt.AttemptID); !errors.Is(err, review.ErrOperatorUnauthorized) {
		t.Fatalf("foreign actor: %v", err)
	}
	for _, denied := range []string{
		strings.Replace(raw, `"repository_id":"`+i.Attempt.Change.RepositoryID+`"`, `"repository_id":"other/repo"`, 1),
		strings.Replace(raw, `"roles":["operator"]`, `"roles":["publisher"]`, 1),
	} {
		d, err := review.DecodeOperatorDirectory([]byte(denied), func(string) string { return "alice-secret" })
		if err != nil {
			t.Fatal(err)
		}
		a.cfg.Operators = d
		actor, _ := d.Authenticate("alice-secret")
		if v, err := a.OperatorAttempt(context.Background(), actor, i.Attempt.AttemptID); !errors.Is(err, review.ErrOperatorForbidden) || v.AttemptID != "" {
			t.Fatalf("unauthorized status exposed: %+v %v", v, err)
		}
	}
	a.cfg.Operators = d
	project := a.cfg.ProjectID
	a.cfg.ProjectID = "different-project"
	if _, err = a.OperatorAttempt(context.Background(), actor, i.Attempt.AttemptID); !errors.Is(err, ErrOperatorAttemptNotFound) {
		t.Fatalf("cross namespace: %v", err)
	}
	a.cfg.ProjectID = project
	if _, err = a.db.Exec(`UPDATE budget_attempts SET active=false WHERE id=$1`, i.Attempt.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE budget_inbox SET deadline=clock_timestamp()-interval '1 second' WHERE run_id=$1`, i.Job.RunID); err != nil {
		t.Fatal(err)
	}
	v, err = a.OperatorAttempt(context.Background(), actor, i.Attempt.AttemptID)
	if err != nil || v.Active || v.InboxState != "uncertain" {
		t.Fatalf("historical status unavailable: %+v %v", v, err)
	}
	if _, err = a.db.Exec(`UPDATE budget_aggregates SET checkpoint_digest='corrupt' WHERE run_id=$1`, i.Job.RunID); err != nil {
		t.Fatal(err)
	}
	if v, err = a.OperatorAttempt(context.Background(), actor, i.Attempt.AttemptID); err == nil || v.AttemptID != "" {
		t.Fatalf("corrupt aggregate projected: %+v %v", v, err)
	}
}
