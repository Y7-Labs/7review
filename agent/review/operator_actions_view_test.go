package review

import (
	"strings"
	"testing"
)

const operatorActionsFixture = `{"schema_version":1,"attempt_id":"attempt-1","actions":[{"action_id":"action-1","state":"result_recorded","provider":"openai_compat","model":"pinned-model","role":"reasoner","step":"review","currency":"USD","route_version":"contract-1","upper":{"input_tokens":100,"output_tokens":20,"money_micro":3,"active_ms":1000},"settled":true,"actual":{"input_tokens":80,"output_tokens":10,"money_micro":2,"active_ms":500},"receipt_id":"settle-1","evidence_ref":"operator:alice:invoice-42","integrity_incident":false,"created_at":"2026-11-01T00:00:00Z"}]}`

func TestOperatorActionsViewRequiresExactClosedBoundedProjection(t *testing.T) {
	view, err := DecodeOperatorActionsView([]byte(operatorActionsFixture), "attempt-1")
	if err != nil || len(view.Actions) != 1 || view.Actions[0].Actual == nil || view.Actions[0].Actual.InputTokens != 80 {
		t.Fatalf("valid action view: %+v %v", view, err)
	}
	invalid := []string{
		`{}`,
		strings.Replace(operatorActionsFixture, `"attempt_id":"attempt-1"`, `"attempt_id":"other"`, 1),
		strings.Replace(operatorActionsFixture, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		strings.Replace(operatorActionsFixture, `"created_at"`, `"unknown":1,"created_at"`, 1),
		strings.Replace(operatorActionsFixture, `"input_tokens":80`, `"input_tokens":80,"extra":1`, 1),
		strings.Replace(operatorActionsFixture, `"settled":true`, `"settled":false`, 1),
		strings.Replace(operatorActionsFixture, `"state":"result_recorded"`, `"state":"prepared"`, 1),
		strings.Replace(operatorActionsFixture, `"money_micro":3`, `"money_micro":-1`, 1),
		strings.Replace(operatorActionsFixture, `"evidence_ref":"operator:alice:invoice-42"`, `"evidence_ref":""`, 1),
	}
	for _, raw := range invalid {
		if _, err := DecodeOperatorActionsView([]byte(raw), "attempt-1"); err == nil {
			t.Fatalf("unsafe action projection accepted: %s", raw)
		}
	}
}

func TestOperatorActionsViewRejectsAmbiguousOrderAndOversizedLists(t *testing.T) {
	action := strings.TrimSuffix(strings.TrimPrefix(operatorActionsFixture[strings.Index(operatorActionsFixture, `[{`):], `[`), `]}`)
	unsorted := `{"schema_version":1,"attempt_id":"attempt-1","actions":[` + strings.Replace(action, `"action_id":"action-1"`, `"action_id":"action-2"`, 1) + `,` + action + `]}`
	if _, err := DecodeOperatorActionsView([]byte(unsorted), "attempt-1"); err == nil {
		t.Fatal("unsorted action view accepted")
	}
	actions := make([]string, MaxOperatorActions+1)
	for i := range actions {
		actions[i] = action
	}
	oversized := `{"schema_version":1,"attempt_id":"attempt-1","actions":[` + strings.Join(actions, ",") + `]}`
	if _, err := DecodeOperatorActionsView([]byte(oversized), "attempt-1"); err == nil {
		t.Fatal("oversized action view accepted")
	}
}
