package review

import (
	"encoding/json"
	"errors"
	"math"
	"time"
	"unicode/utf8"
)

type FindingDecision string

const (
	FindingAccepted FindingDecision = "accepted"
	FindingRejected FindingDecision = "rejected"
	FindingDisputed FindingDecision = "disputed"
)

type FindingDispositionPayload struct {
	FindingID     string          `json:"finding_id"`
	FindingDigest string          `json:"finding_digest"`
	Decision      FindingDecision `json:"decision"`
	Reason        string          `json:"reason"`
}

// Actor is deliberately absent: only the authenticated transport can supply it.
type FindingDispositionCommand struct {
	SchemaVersion   int                       `json:"schema_version"`
	CommandID       string                    `json:"command_id"`
	AttemptID       string                    `json:"attempt_id"`
	ExpectedVersion uint64                    `json:"expected_version"`
	Revision        string                    `json:"revision"`
	Kind            string                    `json:"kind"`
	Payload         FindingDispositionPayload `json:"payload"`
}

func (c FindingDispositionCommand) Validate() error {
	if c.SchemaVersion != 1 || !operatorID.MatchString(c.CommandID) || !operatorID.MatchString(c.AttemptID) || c.ExpectedVersion == 0 || c.ExpectedVersion >= math.MaxInt64 || c.Kind != "disposition_finding" || !validOperatorCommandText(c.Revision, 256) || !operatorID.MatchString(c.Payload.FindingID) || !canonicalDigestPattern.MatchString(c.Payload.FindingDigest) || !validOperatorCommandText(c.Payload.Reason, 2048) {
		return errors.New("invalid disposition_finding command")
	}
	switch c.Payload.Decision {
	case FindingAccepted, FindingRejected, FindingDisputed:
		return nil
	default:
		return errors.New("invalid finding decision")
	}
}

func DecodeFindingDispositionCommand(data []byte) (FindingDispositionCommand, error) {
	var c FindingDispositionCommand
	invalid := errors.New("invalid disposition_finding envelope")
	if len(data) == 0 || len(data) > MaxOperatorCommandBytes || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil {
		return c, invalid
	}
	if decodeOperatorObject(data, &c, "schema_version", "command_id", "attempt_id", "expected_version", "revision", "kind", "payload") != nil {
		return FindingDispositionCommand{}, invalid
	}
	var raw struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(data, &raw) != nil || decodeOperatorObject(raw.Payload, &c.Payload, "finding_id", "finding_digest", "decision", "reason") != nil {
		return FindingDispositionCommand{}, invalid
	}
	if err := c.Validate(); err != nil {
		return FindingDispositionCommand{}, err
	}
	return c, nil
}

type FindingDispositionReceipt struct {
	SchemaVersion  int             `json:"schema_version"`
	CommandID      string          `json:"command_id"`
	CommandDigest  string          `json:"command_digest"`
	AttemptID      string          `json:"attempt_id"`
	ActorID        string          `json:"actor_id"`
	Kind           string          `json:"kind"`
	Revision       string          `json:"revision"`
	ControlVersion uint64          `json:"control_version"`
	FindingID      string          `json:"finding_id"`
	FindingDigest  string          `json:"finding_digest"`
	Decision       FindingDecision `json:"decision"`
	State          string          `json:"state"`
	CreatedAt      time.Time       `json:"created_at"`
}

func DecodeFindingDispositionReceipt(data []byte, c FindingDispositionCommand) (FindingDispositionReceipt, error) {
	var r FindingDispositionReceipt
	invalid := errors.New("invalid finding disposition receipt")
	if c.Validate() != nil || len(data) > 16384 || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil || decodeOperatorObject(data, &r, "schema_version", "command_id", "command_digest", "attempt_id", "actor_id", "kind", "revision", "control_version", "finding_id", "finding_digest", "decision", "state", "created_at") != nil {
		return FindingDispositionReceipt{}, invalid
	}
	if r.SchemaVersion != 1 || r.CommandID != c.CommandID || r.AttemptID != c.AttemptID || r.Kind != c.Kind || r.Revision != c.Revision || r.ControlVersion != c.ExpectedVersion+1 || r.FindingID != c.Payload.FindingID || r.FindingDigest != c.Payload.FindingDigest || r.Decision != c.Payload.Decision || r.State != "recorded" || !operatorID.MatchString(r.ActorID) || !canonicalDigestPattern.MatchString(r.CommandDigest) || r.CreatedAt.IsZero() {
		return FindingDispositionReceipt{}, invalid
	}
	return r, nil
}
