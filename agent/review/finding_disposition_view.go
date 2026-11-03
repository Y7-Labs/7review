package review

import (
	"encoding/json"
	"errors"
	"math"
	"time"
	"unicode/utf8"
)

const MaxFindingDispositionViewBytes = 64 << 10

type CurrentFindingDisposition struct {
	CommandID       string          `json:"command_id"`
	ActorID         string          `json:"actor_id"`
	Decision        FindingDecision `json:"decision"`
	Reason          string          `json:"reason"`
	RecordedVersion uint64          `json:"recorded_version"`
	CreatedAt       time.Time       `json:"created_at"`
}

type FindingDispositionView struct {
	SchemaVersion  int                        `json:"schema_version"`
	AttemptID      string                     `json:"attempt_id"`
	Revision       string                     `json:"revision"`
	ControlVersion uint64                     `json:"control_version"`
	Finding        Finding                    `json:"finding"`
	FindingDigest  string                     `json:"finding_digest"`
	Current        *CurrentFindingDisposition `json:"current_disposition"`
}

func ValidateOperatorFindingID(id string) error {
	if !operatorID.MatchString(id) {
		return errors.New("exact finding ID required")
	}
	return nil
}

func DecodeFindingDispositionView(data []byte, attemptID, findingID string) (FindingDispositionView, error) {
	var view FindingDispositionView
	invalid := errors.New("invalid finding disposition view")
	if ValidateOperatorAttemptID(attemptID) != nil || ValidateOperatorFindingID(findingID) != nil || len(data) == 0 || len(data) > MaxFindingDispositionViewBytes || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil {
		return view, invalid
	}
	var outer map[string]json.RawMessage
	if json.Unmarshal(data, &outer) != nil || len(outer) != 7 {
		return FindingDispositionView{}, invalid
	}
	for _, key := range []string{"schema_version", "attempt_id", "revision", "control_version", "finding", "finding_digest", "current_disposition"} {
		if _, ok := outer[key]; !ok {
			return FindingDispositionView{}, invalid
		}
	}
	for _, key := range []string{"schema_version", "attempt_id", "revision", "control_version", "finding", "finding_digest"} {
		if string(outer[key]) == "null" {
			return FindingDispositionView{}, invalid
		}
	}
	if json.Unmarshal(data, &view) != nil {
		return FindingDispositionView{}, invalid
	}
	var raw struct {
		Finding json.RawMessage `json:"finding"`
		Current json.RawMessage `json:"current_disposition"`
	}
	if json.Unmarshal(data, &raw) != nil || decodeClosedFindingObject(raw.Finding, &view.Finding, []string{"ID", "Severity", "Title", "Description", "Suggestion", "Location", "Confidence"}, []string{"finding_type", "strength", "evidence_authority", "citations", "validation_status", "validation_reason"}) != nil {
		return FindingDispositionView{}, invalid
	}
	var findingRaw struct {
		Location  json.RawMessage   `json:"Location"`
		Citations []json.RawMessage `json:"citations"`
	}
	if json.Unmarshal(raw.Finding, &findingRaw) != nil || decodeOperatorObject(findingRaw.Location, &view.Finding.Location, "Path", "Line") != nil {
		return FindingDispositionView{}, invalid
	}
	for index, citation := range findingRaw.Citations {
		if index >= len(view.Finding.Citations) || decodeClosedFindingObject(citation, &view.Finding.Citations[index], []string{"source", "rule", "violation"}, []string{"heading_or_key"}) != nil {
			return FindingDispositionView{}, invalid
		}
	}
	if string(raw.Current) != "null" && decodeOperatorObject(raw.Current, view.Current, "command_id", "actor_id", "decision", "reason", "recorded_version", "created_at") != nil {
		return FindingDispositionView{}, invalid
	}
	digest, err := CanonicalFindingDigest(view.Finding)
	if err != nil || view.SchemaVersion != 1 || view.AttemptID != attemptID || view.Finding.ID != findingID || !validOperatorCommandText(view.Revision, 256) || view.ControlVersion == 0 || view.ControlVersion > math.MaxInt64 || view.FindingDigest != digest {
		return FindingDispositionView{}, invalid
	}
	if view.Current != nil {
		current := view.Current
		if !operatorID.MatchString(current.CommandID) || !operatorID.MatchString(current.ActorID) || !validOperatorCommandText(current.Reason, 2048) || current.RecordedVersion == 0 || current.RecordedVersion > view.ControlVersion || current.CreatedAt.IsZero() {
			return FindingDispositionView{}, invalid
		}
		switch current.Decision {
		case FindingAccepted, FindingRejected, FindingDisputed:
		default:
			return FindingDispositionView{}, invalid
		}
	}
	return view, nil
}

func decodeClosedFindingObject(data []byte, target any, required, optional []string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
		if value, ok := raw[key]; !ok || string(value) == "null" {
			return errors.New("missing finding view field")
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key, value := range raw {
		if !allowed[key] || string(value) == "null" {
			return errors.New("invalid finding view field")
		}
	}
	return json.Unmarshal(data, target)
}
