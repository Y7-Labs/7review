package review

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxOperatorCommandBytes = 8192

type CancelAttemptPayload struct {
	Reason string `json:"reason"`
}

// Actor is deliberately absent: only the authenticated transport can supply it.
type CancelAttemptCommand struct {
	SchemaVersion   int                  `json:"schema_version"`
	CommandID       string               `json:"command_id"`
	AttemptID       string               `json:"attempt_id"`
	ExpectedVersion uint64               `json:"expected_version"`
	Revision        string               `json:"revision"`
	Kind            string               `json:"kind"`
	Payload         CancelAttemptPayload `json:"payload"`
}

func (c CancelAttemptCommand) Validate() error {
	if c.SchemaVersion != 1 || !operatorID.MatchString(c.CommandID) || !operatorID.MatchString(c.AttemptID) || c.ExpectedVersion == 0 || c.ExpectedVersion >= math.MaxInt64 || c.Kind != "cancel_attempt" || !validOperatorCommandText(c.Revision, 256) || !validOperatorCommandText(c.Payload.Reason, 2048) {
		return errors.New("invalid cancel_attempt command")
	}
	return nil
}

func validOperatorCommandText(value string, limit int) bool {
	// encoding/json replaces unpaired surrogates with RuneError. Do not let
	// lossy text silently collapse distinct audit payloads into one command.
	return strings.TrimSpace(value) != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && !strings.ContainsRune(value, utf8.RuneError)
}

func DecodeCancelAttemptCommand(data []byte) (CancelAttemptCommand, error) {
	var c CancelAttemptCommand
	invalid := errors.New("invalid cancel_attempt envelope")
	if len(data) == 0 || len(data) > MaxOperatorCommandBytes || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil {
		return c, invalid
	}
	if decodeOperatorObject(data, &c, "schema_version", "command_id", "attempt_id", "expected_version", "revision", "kind", "payload") != nil {
		return CancelAttemptCommand{}, invalid
	}
	var raw struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(data, &raw) != nil || decodeOperatorObject(raw.Payload, &c.Payload, "reason") != nil {
		return CancelAttemptCommand{}, invalid
	}
	if err := c.Validate(); err != nil {
		return CancelAttemptCommand{}, err
	}
	return c, nil
}

type CancelAttemptReceipt struct {
	SchemaVersion          int       `json:"schema_version"`
	CommandID              string    `json:"command_id"`
	CommandDigest          string    `json:"command_digest"`
	AttemptID              string    `json:"attempt_id"`
	ActorID                string    `json:"actor_id"`
	Kind                   string    `json:"kind"`
	Revision               string    `json:"revision"`
	ControlVersion         uint64    `json:"control_version"`
	State                  string    `json:"state"`
	ReservationDisposition string    `json:"reservation_disposition"`
	CreatedAt              time.Time `json:"created_at"`
}
