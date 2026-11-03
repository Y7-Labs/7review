package review

import (
	"encoding/json"
	"strings"
	"testing"
)

const usageSettlementCommandFixture = `{"schema_version":1,"command_id":"settle-1","action_id":"action-1","kind":"settle_model_usage","payload":{"input_tokens":120,"output_tokens":30,"money_micro":17,"active_ms":250,"evidence_id":"provider-invoice-42","reason":"reconciled against the provider invoice"}}`

func TestUsageSettlementCommandRejectsAmbiguousOrUnsafeInput(t *testing.T) {
	command, err := DecodeUsageSettlementCommand([]byte(usageSettlementCommandFixture))
	if err != nil || command.CommandID != "settle-1" || command.ActionID != "action-1" || command.Payload.OutputTokens != 30 {
		t.Fatalf("valid settlement command: %#v %v", command, err)
	}
	for _, invalid := range []string{
		``,
		`{"schema_version":1,"command_id":"settle-1","command_id":"settle-2","action_id":"action-1","kind":"settle_model_usage","payload":{"input_tokens":1,"output_tokens":1,"money_micro":1,"active_ms":1,"evidence_id":"e","reason":"r"}}`,
		strings.Replace(usageSettlementCommandFixture, `"action_id":"action-1",`, `"action_id":"action-1","actor_id":"alice",`, 1),
		strings.Replace(usageSettlementCommandFixture, `"input_tokens":120`, `"input_tokens":-1`, 1),
		strings.Replace(usageSettlementCommandFixture, `"evidence_id":"provider-invoice-42"`, `"evidence_id":""`, 1),
		strings.Replace(usageSettlementCommandFixture, `"reason":"reconciled against the provider invoice"`, `"reason":""`, 1),
		usageSettlementCommandFixture + `{}`,
	} {
		if got, err := DecodeUsageSettlementCommand([]byte(invalid)); err == nil || got.CommandID != "" {
			t.Fatalf("unsafe settlement command accepted: %s %#v %v", invalid, got, err)
		}
	}
}

func TestUsageSettlementReceiptBindsCommandActorAndActualUsage(t *testing.T) {
	command, _ := DecodeUsageSettlementCommand([]byte(usageSettlementCommandFixture))
	digest, err := CanonicalUsageSettlementCommandDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	receipt := UsageSettlementReceipt{SchemaVersion: 1, CommandID: command.CommandID, CommandDigest: digest, ActionID: command.ActionID, ActorID: "alice", Kind: command.Kind, Actual: command.Payload.Actual(), EvidenceRef: "operator:alice:" + command.Payload.EvidenceID, State: "settled"}
	raw, _ := json.Marshal(receipt)
	decoded, err := DecodeUsageSettlementReceipt(raw, command)
	if err != nil || decoded != receipt {
		t.Fatalf("valid settlement receipt: %#v %v", decoded, err)
	}
	for _, changed := range [][]byte{
		[]byte(strings.Replace(string(raw), `"command_digest":"`+digest+`"`, `"command_digest":"sha256:`+strings.Repeat("f", 64)+`"`, 1)),
		[]byte(strings.Replace(string(raw), `"actor_id":"alice"`, `"actor_id":""`, 1)),
		[]byte(strings.Replace(string(raw), `"output_tokens":30`, `"output_tokens":31`, 1)),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
	} {
		if got, err := DecodeUsageSettlementReceipt(changed, command); err == nil || got.CommandID != "" {
			t.Fatalf("changed settlement receipt accepted: %s %#v %v", changed, got, err)
		}
	}
}
