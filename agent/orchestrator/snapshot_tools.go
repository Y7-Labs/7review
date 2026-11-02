package orchestrator

import "github.com/Y4NN777/7review/agent/review"

type SnapshotToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Surface     string         `json:"surface"`
	InputSchema map[string]any `json:"input_schema"`
}

// SnapshotToolSchemas returns fresh maps; it is a closed compiled vocabulary,
// not a registry populated by model output, profiles or external executors.
func SnapshotToolSchemas() []SnapshotToolSchema {
	definitions := []SnapshotToolSchema{
		{Name: "get_merge_request", Description: "Fetch normalized merge/pull request metadata for the active run."},
		{Name: "get_changed_files", Description: "Fetch changed file metadata and patch availability for the active run."},
		{Name: "list_discussions", Description: "Fetch normalized SCM discussions already known for the active run."},
		{Name: "get_diff_summary", Description: "Fetch normalized diff file summaries, token estimates, and patch line counts."},
		{Name: "get_selected_context", Description: "Fetch selected skills, repository knowledge, evidence reasons, and memory availability."},
		{Name: "get_inline_positions", Description: "Fetch provider inline-comment path, side, line, and diff-ref metadata for changed new-side lines."},
	}
	for n := range definitions {
		definitions[n].Surface = review.SnapshotToolSurface(definitions[n].Name)
		definitions[n].InputSchema = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"run": map[string]any{"type": "string", "description": "Optional active run ID; omitted inside the review loop."}}}
	}
	return definitions
}

func AvailableSnapshotToolSchemas(source review.Source) []SnapshotToolSchema {
	out := make([]SnapshotToolSchema, 0)
	if !source.Policy.AllowsCapability("repo.read") {
		return out
	}
	for _, schema := range SnapshotToolSchemas() {
		allowed := false
		for _, skill := range source.SkillActivations {
			for _, surface := range skill.AllowedTools {
				if surface == schema.Surface {
					allowed = true
				}
			}
		}
		if allowed {
			out = append(out, schema)
		}
	}
	return out
}

func SnapshotToolDefinitions(source review.Source) []ToolDefinition {
	out := make([]ToolDefinition, 0)
	for _, schema := range AvailableSnapshotToolSchemas(source) {
		out = append(out, ToolDefinition{Name: schema.Name, Description: schema.Description, InputSchema: schema.InputSchema})
	}
	return out
}
