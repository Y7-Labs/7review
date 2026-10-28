package review

import "time"

type ResourceLimits struct {
	Currency string             `json:"currency,omitempty" yaml:"currency,omitempty"`
	Attempt  ResourceLimitScope `json:"attempt" yaml:"attempt"`
	Change   ResourceLimitScope `json:"change" yaml:"change"`
	Project  ResourceLimitScope `json:"project" yaml:"project"`
}

type ResourceLimitScope struct {
	ModelCalls       int64              `json:"model_calls" yaml:"model_calls"`
	ToolCalls        int64              `json:"tool_calls" yaml:"tool_calls"`
	InputTokens      int64              `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens     int64              `json:"output_tokens" yaml:"output_tokens"`
	MoneyMicro       int64              `json:"money_micro" yaml:"money_micro"`
	ActiveMS         int64              `json:"active_ms" yaml:"active_ms"`
	ObservationBytes int64              `json:"observation_bytes" yaml:"observation_bytes"`
	Period           *FixedBudgetPeriod `json:"period,omitempty" yaml:"period,omitempty"`
}

type FixedBudgetPeriod struct {
	Kind       string    `json:"kind" yaml:"kind"`
	Anchor     time.Time `json:"anchor" yaml:"anchor"`
	DurationMS int64     `json:"duration_ms" yaml:"duration_ms"`
}

func (limits ResourceLimits) Clone() ResourceLimits {
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
