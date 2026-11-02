package review

import (
	"errors"
	"math"
	"net/url"
	"time"
)

// OperatorAttemptView is status, not a dispatch/publication permit. Its control
// version advances on dispositions, separately from immutable admission identity.
type OperatorAttemptView struct {
	AttemptID                 string    `json:"attempt_id"`
	RunID                     string    `json:"run_id"`
	Revision                  string    `json:"revision"`
	InboxState                string    `json:"inbox_state"`
	JobPhase                  string    `json:"job_phase"`
	Current                   bool      `json:"current"`
	Active                    bool      `json:"active"`
	Deadline                  time.Time `json:"deadline"`
	ControlVersion            uint64    `json:"control_version"`
	ReleasedUnusedActions     int64     `json:"released_unused_actions"`
	RetainedModelReservations int64     `json:"retained_model_reservations"`
}

func ValidateOperatorAttemptID(id string) error {
	if !operatorID.MatchString(id) {
		return errors.New("exact attempt ID required")
	}
	return nil
}

func DecodeOperatorAttemptView(data []byte, expected string) (OperatorAttemptView, error) {
	var v OperatorAttemptView
	invalid := errors.New("invalid operator attempt response")
	if ValidateOperatorAttemptID(expected) != nil || len(data) > 16384 || rejectDuplicateJSONKeys(data) != nil || decodeOperatorObject(data, &v, "attempt_id", "run_id", "revision", "inbox_state", "job_phase", "current", "active", "deadline", "control_version", "released_unused_actions", "retained_model_reservations") != nil {
		return OperatorAttemptView{}, invalid
	}
	if v.AttemptID != expected || !operatorID.MatchString(v.RunID) || !validOperatorCommandText(v.Revision, 256) || v.Deadline.IsZero() || v.ControlVersion == 0 || v.ControlVersion > math.MaxInt64 || v.ReleasedUnusedActions < 0 || v.RetainedModelReservations < 0 {
		return OperatorAttemptView{}, invalid
	}
	switch v.InboxState {
	case "pending", "running", "completed", "failed", "expired", "uncertain", "cancelled", "superseded":
	default:
		return OperatorAttemptView{}, invalid
	}
	switch v.JobPhase {
	case "accepted", "acquiring", "admitted", "effect_started", "finished", "legacy_unknown":
	default:
		return OperatorAttemptView{}, invalid
	}
	if v.InboxState == "cancelled" && (v.Active || v.JobPhase != "finished") {
		return OperatorAttemptView{}, invalid
	}
	if v.InboxState == "superseded" && (v.Active || v.Current || v.JobPhase != "finished") {
		return OperatorAttemptView{}, invalid
	}
	return v, nil
}

func DecodeCancelAttemptReceipt(data []byte, c CancelAttemptCommand) (CancelAttemptReceipt, error) {
	var r CancelAttemptReceipt
	invalid := errors.New("invalid cancellation receipt")
	if c.Validate() != nil || len(data) > 16384 || rejectDuplicateJSONKeys(data) != nil || decodeOperatorObject(data, &r, "schema_version", "command_id", "command_digest", "attempt_id", "actor_id", "kind", "revision", "control_version", "state", "reservation_disposition", "created_at") != nil {
		return CancelAttemptReceipt{}, invalid
	}
	if r.SchemaVersion != 1 || r.CommandID != c.CommandID || r.AttemptID != c.AttemptID || r.Kind != "cancel_attempt" || r.Revision != c.Revision || r.ControlVersion != c.ExpectedVersion+1 || r.State != "cancelled" || r.ReservationDisposition != "retained" || !operatorID.MatchString(r.ActorID) || !canonicalDigestPattern.MatchString(r.CommandDigest) || r.CreatedAt.IsZero() {
		return CancelAttemptReceipt{}, invalid
	}
	return r, nil
}

type OperatorCommandConflictView struct {
	CurrentAttemptID  string `json:"current_attempt_id"`
	ControlVersion    uint64 `json:"control_version"`
	Reason            string `json:"reason"`
	CurrentAttemptURL string `json:"current_attempt_url"`
}

func DecodeOperatorCommandConflict(data []byte) (OperatorCommandConflictView, error) {
	var v OperatorCommandConflictView
	invalid := errors.New("invalid operator conflict response")
	if len(data) > 16384 || rejectDuplicateJSONKeys(data) != nil || decodeOperatorObject(data, &v, "current_attempt_id", "control_version", "reason", "current_attempt_url") != nil || ValidateOperatorAttemptID(v.CurrentAttemptID) != nil || v.ControlVersion == 0 || v.ControlVersion > math.MaxInt64 || !validOperatorCommandText(v.Reason, 2048) {
		return OperatorCommandConflictView{}, invalid
	}
	u, err := url.Parse(v.CurrentAttemptURL)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Path != "/operator/attempt" || u.RawPath != "" || u.Fragment != "" {
		return OperatorCommandConflictView{}, invalid
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["attempt_id"]) != 1 || q.Get("attempt_id") != v.CurrentAttemptID {
		return OperatorCommandConflictView{}, invalid
	}
	return v, nil
}
