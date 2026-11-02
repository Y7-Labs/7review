package review

import (
	"strings"
	"testing"
)

const operatorViewFixture = `{"attempt_id":"attempt-1","run_id":"run-1","revision":"head","inbox_state":"cancelled","job_phase":"finished","current":true,"active":false,"deadline":"2026-01-01T00:00:00Z","control_version":3,"released_unused_actions":1,"retained_model_reservations":0}`

func TestOperatorViewRejectsAmbiguousIncompleteAndWrongIdentityResponses(t *testing.T) {
	if v, err := DecodeOperatorAttemptView([]byte(operatorViewFixture), "attempt-1"); err != nil || v.ReleasedUnusedActions != 1 {
		t.Fatalf("view: %+v %v", v, err)
	}
	for _, raw := range []string{"{}", operatorViewFixture + "{}", strings.Replace(operatorViewFixture, `"active":false`, `"active":false,"active":true`, 1), strings.Replace(operatorViewFixture, `"active":false`, `"active":true`, 1), strings.Replace(operatorViewFixture, `"control_version":3`, `"control_version":0`, 1), strings.Replace(operatorViewFixture, `"released_unused_actions":1`, `"released_unused_actions":-1`, 1), strings.Replace(operatorViewFixture, `"job_phase":"finished"`, `"job_phase":"made_up"`, 1)} {
		if _, err := DecodeOperatorAttemptView([]byte(raw), "attempt-1"); err == nil {
			t.Fatal("unproven view accepted")
		}
	}
	if _, err := DecodeOperatorAttemptView([]byte(operatorViewFixture), "other"); err == nil {
		t.Fatal("different attempt accepted")
	}
}

func TestCancellationReceiptMustAcknowledgeTheExactSubmittedCommand(t *testing.T) {
	c, err := DecodeCancelAttemptCommand([]byte(cancelCommandFixture))
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"schema_version":1,"command_id":"cancel-1","command_digest":"sha256:` + strings.Repeat("a", 64) + `","attempt_id":"attempt-1","actor_id":"alice","kind":"cancel_attempt","revision":"head","control_version":2,"state":"cancelled","reservation_disposition":"retained","created_at":"2026-01-01T00:00:00Z"}`
	if _, err := DecodeCancelAttemptReceipt([]byte(raw), c); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"{}", raw + "{}", strings.Replace(raw, `"command_id":"cancel-1"`, `"command_id":"different"`, 1), strings.Replace(raw, `"attempt_id":"attempt-1"`, `"attempt_id":"different"`, 1), strings.Replace(raw, `"control_version":2`, `"control_version":3`, 1), strings.Replace(raw, `"state":"cancelled"`, `"state":"completed"`, 1), strings.Replace(raw, `"actor_id":"alice"`, `"actor_id":""`, 1), strings.Replace(raw, `"state":"cancelled"`, `"state":"cancelled","state":"completed"`, 1)} {
		if _, err := DecodeCancelAttemptReceipt([]byte(bad), c); err == nil {
			t.Fatal("unproven cancellation receipt accepted")
		}
	}
}
