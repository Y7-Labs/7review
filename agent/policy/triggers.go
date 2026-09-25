package policy

type TriggerInput struct {
	IsUpdate bool
	IsDraft  bool
	Branch   string
	Author   string
	Labels   []string
}

type TriggerDecision struct {
	Accepted bool     `json:"accepted"`
	Reasons  []string `json:"reasons"`
}

func EvaluateTrigger(triggers TriggersV2, input TriggerInput) TriggerDecision {
	if !triggers.Enabled {
		return TriggerDecision{Reasons: []string{"automatic triggers are disabled"}}
	}
	if input.IsUpdate && !triggers.OnUpdates {
		return TriggerDecision{Reasons: []string{"updates are excluded"}}
	}
	if input.IsDraft && !triggers.IncludeDrafts {
		return TriggerDecision{Reasons: []string{"draft changes are excluded"}}
	}
	if anyGlob(triggers.ExcludeBranches, []string{input.Branch}) {
		return TriggerDecision{Reasons: []string{"branch matches an exclusion"}}
	}
	if contains(triggers.ExcludeAuthors, input.Author) {
		return TriggerDecision{Reasons: []string{"author matches an exclusion"}}
	}
	if intersects(triggers.ExcludeLabels, input.Labels) {
		return TriggerDecision{Reasons: []string{"label matches an exclusion"}}
	}
	if len(triggers.IncludeBranches) > 0 && !anyGlob(triggers.IncludeBranches, []string{input.Branch}) {
		return TriggerDecision{Reasons: []string{"branch does not match the include set"}}
	}
	if len(triggers.IncludeAuthors) > 0 && !contains(triggers.IncludeAuthors, input.Author) {
		return TriggerDecision{Reasons: []string{"author does not match the include set"}}
	}
	if len(triggers.IncludeLabels) > 0 && !intersects(triggers.IncludeLabels, input.Labels) {
		return TriggerDecision{Reasons: []string{"labels do not match the include set"}}
	}
	return TriggerDecision{Accepted: true, Reasons: []string{"all trusted trigger conditions matched"}}
}
