package accounting

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/review"
)

func TestPostgresOperatorActionsProjectsOnlyValidatedRepositoryUsage(t *testing.T) {
	a, intent, _ := preparedActionFixture(t)
	if _, err := a.ReserveAndBegin(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	operator, _ := cancellationActor(t, a, intent)

	view, err := a.OperatorActions(context.Background(), operator, intent.Attempt.AttemptID)
	if err != nil || view.AttemptID != intent.Attempt.AttemptID || len(view.Actions) != 1 {
		t.Fatalf("unsettled action view: %+v %v", view, err)
	}
	action := view.Actions[0]
	if action.ActionID != intent.ActionID || action.State != "sending" || action.Provider != intent.Provider || action.Model != intent.Model || action.Role != intent.Role || action.Step != intent.Step || action.Currency != "USD" || action.RouteVersion == "" || action.Settled || action.Actual != nil || action.ReceiptID != "" || action.EvidenceRef != "" {
		t.Fatalf("unsafe or incomplete action projection: %+v", action)
	}
	raw, _ := json.Marshal(view)
	for _, secret := range []string{string(intent.Payload), intent.RequestDigest, "request_digest", "worker_id", "lease", "fence"} {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatalf("action projection leaked %q: %s", secret, raw)
		}
	}

	command := review.UsageSettlementCommand{SchemaVersion: 1, CommandID: "settle-action-view", ActionID: intent.ActionID, Kind: "settle_model_usage", Payload: review.UsageSettlementPayload{InputTokens: 10, OutputTokens: 2, MoneyMicro: 2, ActiveMS: 1, EvidenceID: "invoice-42", Reason: "reconciled invoice"}}
	if _, err = a.SettleUsageByOperator(context.Background(), operator, command); err != nil {
		t.Fatal(err)
	}
	view, err = a.OperatorActions(context.Background(), operator, intent.Attempt.AttemptID)
	if err != nil || len(view.Actions) != 1 || !view.Actions[0].Settled || view.Actions[0].Actual == nil || *view.Actions[0].Actual != command.Payload.Actual() || view.Actions[0].ReceiptID != command.CommandID || view.Actions[0].EvidenceRef != "operator:alice:invoice-42" {
		t.Fatalf("settled action view: %+v %v", view, err)
	}
}

func TestPostgresOperatorActionsRequiresOperatorAndFailsClosedOnCorruption(t *testing.T) {
	a, intent, _ := preparedActionFixture(t)
	if _, err := a.ReserveAndBegin(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	change := intent.Attempt.Change
	raw := `{"schema_version":1,"principals":[{"id":"alice","token_env":"ALICE_TOKEN","grants":[{"boundary_id":"` + a.cfg.BoundaryID + `","project_id":"` + a.cfg.ProjectID + `","provider":"` + change.Provider + `","host":"` + change.Host + `","repository_id":"` + change.RepositoryID + `","roles":["operator"]}]},{"id":"participant","token_env":"PARTICIPANT_TOKEN","grants":[{"boundary_id":"` + a.cfg.BoundaryID + `","project_id":"` + a.cfg.ProjectID + `","provider":"` + change.Provider + `","host":"` + change.Host + `","repository_id":"` + change.RepositoryID + `","roles":["participant"]}]},{"id":"foreign","token_env":"FOREIGN_TOKEN","grants":[{"boundary_id":"` + a.cfg.BoundaryID + `","project_id":"` + a.cfg.ProjectID + `","provider":"` + change.Provider + `","host":"` + change.Host + `","repository_id":"org/other","roles":["operator"]}]}]}`
	directory, err := review.DecodeOperatorDirectory([]byte(raw), func(name string) string { return strings.ToLower(strings.TrimSuffix(name, "_TOKEN")) + "-secret" })
	if err != nil {
		t.Fatal(err)
	}
	a.cfg.Operators = directory
	operator, _ := directory.Authenticate("alice-secret")
	participant, _ := directory.Authenticate("participant-secret")
	foreign, _ := directory.Authenticate("foreign-secret")
	for name, actor := range map[string]review.OperatorActor{"missing": {}, "participant": participant, "foreign": foreign} {
		if view, gotErr := a.OperatorActions(context.Background(), actor, intent.Attempt.AttemptID); gotErr == nil || len(view.Actions) != 0 {
			t.Fatalf("%s actor read actions: %+v %v", name, view, gotErr)
		}
	}
	if _, err = a.db.Exec(`UPDATE budget_actions SET quote='{}' WHERE id=$1`, intent.ActionID); err != nil {
		t.Fatal(err)
	}
	if view, gotErr := a.OperatorActions(context.Background(), operator, intent.Attempt.AttemptID); !errors.Is(gotErr, ErrIntegrity) || len(view.Actions) != 0 {
		t.Fatalf("corrupt action projected: %+v %v", view, gotErr)
	}
}
