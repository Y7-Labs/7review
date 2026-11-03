package review

import (
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

const MaxOperatorActions = 256
const MaxOperatorActionsViewBytes = 256 << 10

type OperatorActionView struct {
	ActionID          string            `json:"action_id"`
	State             string            `json:"state"`
	Provider          string            `json:"provider"`
	Model             string            `json:"model"`
	Role              string            `json:"role"`
	Step              string            `json:"step"`
	Currency          string            `json:"currency"`
	RouteVersion      string            `json:"route_version"`
	Upper             ModelUsageActual  `json:"upper"`
	Settled           bool              `json:"settled"`
	Actual            *ModelUsageActual `json:"actual,omitempty"`
	ReceiptID         string            `json:"receipt_id,omitempty"`
	EvidenceRef       string            `json:"evidence_ref,omitempty"`
	IntegrityIncident bool              `json:"integrity_incident"`
	CreatedAt         time.Time         `json:"created_at"`
}

type OperatorActionsView struct {
	SchemaVersion int                  `json:"schema_version"`
	AttemptID     string               `json:"attempt_id"`
	Actions       []OperatorActionView `json:"actions"`
}

func DecodeOperatorActionsView(data []byte, expectedAttemptID string) (OperatorActionsView, error) {
	invalid := errors.New("invalid operator actions response")
	if ValidateOperatorAttemptID(expectedAttemptID) != nil || len(data) == 0 || len(data) > MaxOperatorActionsViewBytes || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil {
		return OperatorActionsView{}, invalid
	}
	var raw struct {
		SchemaVersion int               `json:"schema_version"`
		AttemptID     string            `json:"attempt_id"`
		Actions       []json.RawMessage `json:"actions"`
	}
	if decodeOperatorObject(data, &raw, "schema_version", "attempt_id", "actions") != nil || raw.SchemaVersion != 1 || raw.AttemptID != expectedAttemptID || len(raw.Actions) > MaxOperatorActions {
		return OperatorActionsView{}, invalid
	}
	view := OperatorActionsView{SchemaVersion: raw.SchemaVersion, AttemptID: raw.AttemptID, Actions: make([]OperatorActionView, 0, len(raw.Actions))}
	for _, encoded := range raw.Actions {
		var action OperatorActionView
		if decodeOperatorActionViewObject(encoded, &action) != nil {
			return OperatorActionsView{}, invalid
		}
		var amounts struct {
			Upper  json.RawMessage `json:"upper"`
			Actual json.RawMessage `json:"actual"`
		}
		if json.Unmarshal(encoded, &amounts) != nil || decodeOperatorObject(amounts.Upper, &action.Upper, "input_tokens", "output_tokens", "money_micro", "active_ms") != nil {
			return OperatorActionsView{}, invalid
		}
		if len(amounts.Actual) != 0 && string(amounts.Actual) != "null" && decodeOperatorObject(amounts.Actual, action.Actual, "input_tokens", "output_tokens", "money_micro", "active_ms") != nil {
			return OperatorActionsView{}, invalid
		}
		if !validOperatorActionView(action) {
			return OperatorActionsView{}, invalid
		}
		if len(view.Actions) != 0 {
			previous := view.Actions[len(view.Actions)-1]
			if action.CreatedAt.Before(previous.CreatedAt) || (action.CreatedAt.Equal(previous.CreatedAt) && action.ActionID <= previous.ActionID) {
				return OperatorActionsView{}, invalid
			}
		}
		view.Actions = append(view.Actions, action)
	}
	return view, nil
}

func decodeOperatorActionViewObject(data []byte, action *OperatorActionView) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return errors.New("invalid operator action")
	}
	required := map[string]bool{"action_id": true, "state": true, "provider": true, "model": true, "role": true, "step": true, "currency": true, "route_version": true, "upper": true, "settled": true, "integrity_incident": true, "created_at": true}
	optional := map[string]bool{"actual": true, "receipt_id": true, "evidence_ref": true}
	for key, value := range fields {
		if (!required[key] && !optional[key]) || string(value) == "null" {
			return errors.New("invalid operator action field")
		}
		delete(required, key)
	}
	if len(required) != 0 {
		return errors.New("missing operator action field")
	}
	return json.Unmarshal(data, action)
}

func validOperatorActionView(action OperatorActionView) bool {
	if !operatorID.MatchString(action.ActionID) || !operatorID.MatchString(action.Provider) || !operatorID.MatchString(action.Model) || !operatorID.MatchString(action.Role) || !operatorID.MatchString(action.Step) || action.Currency == "" || len(action.Currency) > 32 || !operatorID.MatchString(action.RouteVersion) || action.CreatedAt.IsZero() || !nonnegativeActual(action.Upper) {
		return false
	}
	switch action.State {
	case "prepared", "sending", "uncertain", "result_recorded", "not_executed":
	default:
		return false
	}
	if action.Settled {
		return action.State != "prepared" && action.State != "not_executed" && action.Actual != nil && nonnegativeActual(*action.Actual) && operatorID.MatchString(action.ReceiptID) && action.EvidenceRef != "" && len(action.EvidenceRef) <= 4096
	}
	return action.Actual == nil && action.ReceiptID == "" && action.EvidenceRef == ""
}

func nonnegativeActual(actual ModelUsageActual) bool {
	return actual.InputTokens >= 0 && actual.OutputTokens >= 0 && actual.MoneyMicro >= 0 && actual.ActiveMS >= 0
}
