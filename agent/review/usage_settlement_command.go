package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

type ModelUsageActual struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	MoneyMicro   int64 `json:"money_micro"`
	ActiveMS     int64 `json:"active_ms"`
}

type UsageSettlementPayload struct {
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	MoneyMicro   int64  `json:"money_micro"`
	ActiveMS     int64  `json:"active_ms"`
	EvidenceID   string `json:"evidence_id"`
	Reason       string `json:"reason"`
}

func (p UsageSettlementPayload) Actual() ModelUsageActual {
	return ModelUsageActual{InputTokens: p.InputTokens, OutputTokens: p.OutputTokens, MoneyMicro: p.MoneyMicro, ActiveMS: p.ActiveMS}
}

type UsageSettlementCommand struct {
	SchemaVersion int                    `json:"schema_version"`
	CommandID     string                 `json:"command_id"`
	ActionID      string                 `json:"action_id"`
	Kind          string                 `json:"kind"`
	Payload       UsageSettlementPayload `json:"payload"`
}

func (c UsageSettlementCommand) Validate() error {
	if c.SchemaVersion != 1 || !operatorID.MatchString(c.CommandID) || !operatorID.MatchString(c.ActionID) || c.Kind != "settle_model_usage" || c.Payload.InputTokens < 0 || c.Payload.OutputTokens < 0 || c.Payload.MoneyMicro < 0 || c.Payload.ActiveMS < 0 || !operatorID.MatchString(c.Payload.EvidenceID) || !validOperatorCommandText(c.Payload.Reason, 2048) {
		return errors.New("invalid settle_model_usage command")
	}
	return nil
}

func DecodeUsageSettlementCommand(data []byte) (UsageSettlementCommand, error) {
	var c UsageSettlementCommand
	invalid := errors.New("invalid settle_model_usage envelope")
	if len(data) == 0 || len(data) > MaxOperatorCommandBytes || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil || decodeOperatorObject(data, &c, "schema_version", "command_id", "action_id", "kind", "payload") != nil {
		return UsageSettlementCommand{}, invalid
	}
	var raw struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(data, &raw) != nil || decodeOperatorObject(raw.Payload, &c.Payload, "input_tokens", "output_tokens", "money_micro", "active_ms", "evidence_id", "reason") != nil {
		return UsageSettlementCommand{}, invalid
	}
	if err := c.Validate(); err != nil {
		return UsageSettlementCommand{}, err
	}
	return c, nil
}

func CanonicalUsageSettlementCommandDigest(c UsageSettlementCommand) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

type UsageSettlementReceipt struct {
	SchemaVersion int              `json:"schema_version"`
	CommandID     string           `json:"command_id"`
	CommandDigest string           `json:"command_digest"`
	ActionID      string           `json:"action_id"`
	ActorID       string           `json:"actor_id"`
	Kind          string           `json:"kind"`
	Actual        ModelUsageActual `json:"actual"`
	EvidenceRef   string           `json:"evidence_ref"`
	State         string           `json:"state"`
}

func DecodeUsageSettlementReceipt(data []byte, c UsageSettlementCommand) (UsageSettlementReceipt, error) {
	var r UsageSettlementReceipt
	invalid := errors.New("invalid usage settlement receipt")
	if c.Validate() != nil || len(data) == 0 || len(data) > 16384 || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil || decodeOperatorObject(data, &r, "schema_version", "command_id", "command_digest", "action_id", "actor_id", "kind", "actual", "evidence_ref", "state") != nil {
		return UsageSettlementReceipt{}, invalid
	}
	var raw struct {
		Actual json.RawMessage `json:"actual"`
	}
	if json.Unmarshal(data, &raw) != nil || decodeOperatorObject(raw.Actual, &r.Actual, "input_tokens", "output_tokens", "money_micro", "active_ms") != nil {
		return UsageSettlementReceipt{}, invalid
	}
	digest, err := CanonicalUsageSettlementCommandDigest(c)
	if err != nil || r.SchemaVersion != 1 || r.CommandID != c.CommandID || r.CommandDigest != digest || r.ActionID != c.ActionID || !operatorID.MatchString(r.ActorID) || r.Kind != c.Kind || r.Actual != c.Payload.Actual() || r.EvidenceRef != "operator:"+r.ActorID+":"+c.Payload.EvidenceID || r.State != "settled" {
		return UsageSettlementReceipt{}, invalid
	}
	return r, nil
}
