package review

import (
	"strings"
	"testing"
)

const cancelCommandFixture = `{"schema_version":1,"command_id":"cancel-1","attempt_id":"attempt-1","expected_version":1,"revision":"head","kind":"cancel_attempt","payload":{"reason":"operator requested stop"}}`

func TestCancelCommandRequiresClosedVersionedEnvelopeWithoutActorClaims(t *testing.T) {
	c, err := DecodeCancelAttemptCommand([]byte(cancelCommandFixture))
	if err != nil || c.CommandID != "cancel-1" || c.ExpectedVersion != 1 || c.Payload.Reason != "operator requested stop" {
		t.Fatalf("command: %+v %v", c, err)
	}
	for n, raw := range []string{
		"null", "{}", cancelCommandFixture + "{}",
		strings.Replace(cancelCommandFixture, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(cancelCommandFixture, `"command_id":"cancel-1"`, `"command_id":"cancel-1","actor":"alice"`, 1),
		strings.Replace(cancelCommandFixture, `"expected_version":1`, `"expected_version":0`, 1),
		strings.Replace(cancelCommandFixture, `"expected_version":1`, `"expected_version":9223372036854775807`, 1),
		strings.Replace(cancelCommandFixture, `"revision":"head"`, `"revision":""`, 1),
		strings.Replace(cancelCommandFixture, `"kind":"cancel_attempt"`, `"kind":"approve"`, 1),
		strings.Replace(cancelCommandFixture, `"expected_version":1`, `"expected_version":1,"expected_version":2`, 1),
		strings.Replace(cancelCommandFixture, `"reason":"operator requested stop"`, `"reason":"x","actor":"alice"`, 1),
		strings.Replace(cancelCommandFixture, `"reason":"operator requested stop"`, `"reason":"  "`, 1),
		strings.Replace(cancelCommandFixture, `"payload":{"reason":"operator requested stop"}`, `"payload":null`, 1),
		strings.Replace(cancelCommandFixture, `"reason":"operator requested stop"`, `"reason":"`+strings.Repeat("x", 2049)+`"`, 1),
		strings.Repeat(" ", 8193),
	} {
		if _, err := DecodeCancelAttemptCommand([]byte(raw)); err == nil {
			t.Errorf("unsafe command %d accepted", n)
		}
	}
}
