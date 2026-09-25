package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

func validateJSONShape(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := rejectDuplicateJSONKeys(decoder, "$", map[string]any{}); err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("policy: decode JSON shape: %w", err)
	}
	return validateRequiredShape(raw)
}

func rejectDuplicateJSONKeys(decoder *json.Decoder, location string, _ map[string]any) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("policy: decode JSON: %w", err)
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return fmt.Errorf("policy: decode JSON: %w", err)
				}
				key := keyToken.(string)
				if seen[key] {
					return fmt.Errorf("policy: duplicate JSON key %q at %s", key, location)
				}
				seen[key] = true
				if err := rejectDuplicateJSONKeys(decoder, location+"."+key, nil); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			index := 0
			for decoder.More() {
				if err := rejectDuplicateJSONKeys(decoder, fmt.Sprintf("%s[%d]", location, index), nil); err != nil {
					return err
				}
				index++
			}
			_, err = decoder.Token()
			return err
		}
	}
	return nil
}

func validateYAMLShape(data []byte) error {
	var raw map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("policy: decode YAML shape: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("policy: multiple YAML documents are not allowed")
		}
		return fmt.Errorf("policy: decode trailing YAML: %w", err)
	}
	return validateRequiredShape(raw)
}

func validateRequiredShape(root map[string]any) error {
	if err := requireKeys("$", root, "schema_version", "defaults", "limits", "triggers", "capabilities", "domains", "modules", "features", "delegations", "packs", "quality_gate", "retention"); err != nil {
		return err
	}
	defaults, err := objectAt(root, "defaults", "$.defaults")
	if err != nil {
		return err
	}
	if err := requireKeys("$.defaults", defaults, "methods", "publication", "gate_mode", "required_checks"); err != nil {
		return err
	}
	limits, err := objectAt(root, "limits", "$.limits")
	if err != nil {
		return err
	}
	if err := requireKeys("$.limits", limits, "attempt", "change", "project"); err != nil {
		return err
	}
	limitFields := []string{"model_calls", "tool_calls", "input_tokens", "output_tokens", "money_micro", "active_ms", "observation_bytes"}
	for _, scopeName := range []string{"attempt", "change", "project"} {
		scope, err := objectAt(limits, scopeName, "$.limits."+scopeName)
		if err != nil {
			return err
		}
		fields := append([]string(nil), limitFields...)
		if scopeName != "attempt" {
			fields = append(fields, "period")
		}
		if err := requireKeys("$.limits."+scopeName, scope, fields...); err != nil {
			return err
		}
		if scopeName != "attempt" {
			period, err := objectAt(scope, "period", "$.limits."+scopeName+".period")
			if err != nil {
				return err
			}
			if err := requireKeys("$.limits."+scopeName+".period", period, "kind", "anchor", "duration_ms"); err != nil {
				return err
			}
		}
	}
	triggers, err := objectAt(root, "triggers", "$.triggers")
	if err != nil {
		return err
	}
	if err := requireKeys("$.triggers", triggers, "enabled", "on_updates", "include_drafts", "include_branches", "exclude_branches", "include_authors", "exclude_authors", "include_labels", "exclude_labels"); err != nil {
		return err
	}
	capabilities, err := objectAt(root, "capabilities", "$.capabilities")
	if err != nil {
		return err
	}
	if err := requireKeys("$.capabilities", capabilities, "allowed", "required"); err != nil {
		return err
	}
	gate, err := objectAt(root, "quality_gate", "$.quality_gate")
	if err != nil {
		return err
	}
	if err := requireKeys("$.quality_gate", gate, "rule_ids", "required_coverage", "min_severity", "min_strength", "baseline_mode", "context_name"); err != nil {
		return err
	}
	retention, err := objectAt(root, "retention", "$.retention")
	if err != nil {
		return err
	}
	if err := requireKeys("$.retention", retention, "audit_days", "source_days", "unresolved_reservation_days"); err != nil {
		return err
	}
	if err := validateArrayObjects(root, "delegations", "$.delegations", []string{"id", "grantor_scope", "target_scope", "replace_rule_ids", "replace_fields", "constraints", "provenance_ref"}); err != nil {
		return err
	}
	return validateArrayObjects(root, "packs", "$.packs", []string{"id", "priority", "match", "methods", "checks", "risk_floor", "independent_review", "publication"})
}

func validateArrayObjects(root map[string]any, key, location string, fields []string) error {
	values, ok := root[key].([]any)
	if !ok {
		return fmt.Errorf("policy: %s must be an array", location)
	}
	for i, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("policy: %s[%d] must be an object", location, i)
		}
		if err := requireKeys(fmt.Sprintf("%s[%d]", location, i), object, fields...); err != nil {
			return err
		}
		if key == "packs" {
			publication, err := objectAt(object, "publication", fmt.Sprintf("%s[%d].publication", location, i))
			if err != nil {
				return err
			}
			if err := requireKeys(fmt.Sprintf("%s[%d].publication", location, i), publication, "mode", "artifact_classes", "manual_required"); err != nil {
				return err
			}
		} else {
			for _, scopeKey := range []string{"grantor_scope", "target_scope"} {
				scope, err := objectAt(object, scopeKey, fmt.Sprintf("%s[%d].%s", location, i, scopeKey))
				if err != nil {
					return err
				}
				if err := requireKeys(fmt.Sprintf("%s[%d].%s", location, i, scopeKey), scope, "kind", "id"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func objectAt(parent map[string]any, key, location string) (map[string]any, error) {
	value, ok := parent[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("policy: %s must be an object", location)
	}
	return value, nil
}

func requireKeys(location string, object map[string]any, keys ...string) error {
	for _, key := range keys {
		if _, exists := object[key]; !exists {
			return fmt.Errorf("policy: required field %s.%s is missing", location, key)
		}
	}
	return nil
}
