package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeOperatorCeilings accepts only the trusted runtime document, never a
// repository default or a partial object whose omitted fields appear unlimited.
func DecodeOperatorCeilings(data []byte) (LimitsV2, error) {
	if len(data) > MaxConfigBytes {
		return LimitsV2{}, fmt.Errorf("operator ceilings exceed byte bound")
	}
	if err := rejectDuplicateJSONKeys(json.NewDecoder(bytes.NewReader(data)), "$", nil); err != nil {
		return LimitsV2{}, err
	}
	var document struct {
		SchemaVersion int      `json:"schema_version"`
		Limits        LimitsV2 `json:"limits"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return LimitsV2{}, fmt.Errorf("operator ceilings: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return LimitsV2{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return LimitsV2{}, err
	}
	if err := requireKeys("$", raw, "schema_version", "limits"); err != nil {
		return LimitsV2{}, err
	}
	if document.SchemaVersion != 1 {
		return LimitsV2{}, fmt.Errorf("operator ceilings schema_version must be 1")
	}
	limits, err := objectAt(raw, "limits", "$.limits")
	if err != nil {
		return LimitsV2{}, err
	}
	if err := requireKeys("$.limits", limits, "attempt", "change", "project"); err != nil {
		return LimitsV2{}, err
	}
	for _, name := range []string{"attempt", "change", "project"} {
		scope, err := objectAt(limits, name, "$.limits."+name)
		if err != nil {
			return LimitsV2{}, err
		}
		if err := requireKeys("$.limits."+name, scope, "model_calls", "tool_calls", "input_tokens", "output_tokens", "money_micro", "active_ms", "observation_bytes"); err != nil {
			return LimitsV2{}, err
		}
		if name != "attempt" {
			p, err := objectAt(scope, "period", "$.limits."+name+".period")
			if err != nil {
				return LimitsV2{}, err
			}
			if err := requireKeys("$.limits."+name+".period", p, "kind", "anchor", "duration_ms"); err != nil {
				return LimitsV2{}, err
			}
		}
	}
	if err := validateLimits(document.Limits); err != nil {
		return LimitsV2{}, err
	}
	return document.Limits.Clone(), nil
}

func (limits LimitsV2) Clone() LimitsV2 {
	if limits.Attempt.Period != nil {
		p := *limits.Attempt.Period
		limits.Attempt.Period = &p
	}
	if limits.Change.Period != nil {
		p := *limits.Change.Period
		limits.Change.Period = &p
	}
	if limits.Project.Period != nil {
		p := *limits.Project.Period
		limits.Project.Period = &p
	}
	return limits
}
