package policy

import "time"

type ReviewConfigV2 struct {
	SchemaVersion     int                 `json:"schema_version" yaml:"schema_version"`
	ProjectID         string              `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	Defaults          DefaultsV2          `json:"defaults" yaml:"defaults"`
	Limits            LimitsV2            `json:"limits" yaml:"limits"`
	Triggers          TriggersV2          `json:"triggers" yaml:"triggers"`
	Capabilities      CapabilitiesV2      `json:"capabilities" yaml:"capabilities"`
	Domains           map[string][]string `json:"domains" yaml:"domains"`
	Modules           map[string][]string `json:"modules" yaml:"modules"`
	Features          map[string][]string `json:"features" yaml:"features"`
	Delegations       []DelegationV2      `json:"delegations" yaml:"delegations"`
	Packs             []MethodPackV2      `json:"packs" yaml:"packs"`
	QualityGate       QualityGateV2       `json:"quality_gate" yaml:"quality_gate"`
	Retention         RetentionV2         `json:"retention" yaml:"retention"`
	ExtensionsAllowed bool                `json:"extensions_allowed,omitempty" yaml:"extensions_allowed,omitempty"`
}

type DefaultsV2 struct {
	Methods               []string `json:"methods" yaml:"methods"`
	ReplaceDefaultMethods bool     `json:"replace_default_methods,omitempty" yaml:"replace_default_methods,omitempty"`
	Publication           string   `json:"publication" yaml:"publication"`
	GateMode              string   `json:"gate_mode" yaml:"gate_mode"`
	RequiredChecks        []string `json:"required_checks" yaml:"required_checks"`
}

type LimitsV2 struct {
	Currency string       `json:"currency,omitempty" yaml:"currency,omitempty"`
	Attempt  LimitScopeV2 `json:"attempt" yaml:"attempt"`
	Change   LimitScopeV2 `json:"change" yaml:"change"`
	Project  LimitScopeV2 `json:"project" yaml:"project"`
}

type LimitScopeV2 struct {
	ModelCalls       int64          `json:"model_calls" yaml:"model_calls"`
	ToolCalls        int64          `json:"tool_calls" yaml:"tool_calls"`
	InputTokens      int64          `json:"input_tokens" yaml:"input_tokens"`
	OutputTokens     int64          `json:"output_tokens" yaml:"output_tokens"`
	MoneyMicro       int64          `json:"money_micro" yaml:"money_micro"`
	ActiveMS         int64          `json:"active_ms" yaml:"active_ms"`
	ObservationBytes int64          `json:"observation_bytes" yaml:"observation_bytes"`
	Period           *FixedPeriodV2 `json:"period,omitempty" yaml:"period,omitempty"`
}

type FixedPeriodV2 struct {
	Kind       string    `json:"kind" yaml:"kind"`
	Anchor     time.Time `json:"anchor" yaml:"anchor"`
	DurationMS int64     `json:"duration_ms" yaml:"duration_ms"`
}

type TriggersV2 struct {
	Enabled         bool     `json:"enabled" yaml:"enabled"`
	OnUpdates       bool     `json:"on_updates" yaml:"on_updates"`
	IncludeDrafts   bool     `json:"include_drafts" yaml:"include_drafts"`
	IncludeBranches []string `json:"include_branches" yaml:"include_branches"`
	ExcludeBranches []string `json:"exclude_branches" yaml:"exclude_branches"`
	IncludeAuthors  []string `json:"include_authors" yaml:"include_authors"`
	ExcludeAuthors  []string `json:"exclude_authors" yaml:"exclude_authors"`
	IncludeLabels   []string `json:"include_labels" yaml:"include_labels"`
	ExcludeLabels   []string `json:"exclude_labels" yaml:"exclude_labels"`
}

type CapabilitiesV2 struct {
	Allowed  []string `json:"allowed" yaml:"allowed"`
	Required []string `json:"required" yaml:"required"`
}

type ScopeRefV2 struct {
	Kind string `json:"kind" yaml:"kind"`
	ID   string `json:"id" yaml:"id"`
}

type DelegationV2 struct {
	ID             string                  `json:"id" yaml:"id"`
	GrantorScope   ScopeRefV2              `json:"grantor_scope" yaml:"grantor_scope"`
	TargetScope    ScopeRefV2              `json:"target_scope" yaml:"target_scope"`
	ReplaceRuleIDs []string                `json:"replace_rule_ids" yaml:"replace_rule_ids"`
	ReplaceFields  []string                `json:"replace_fields" yaml:"replace_fields"`
	Constraints    DelegationConstraintsV2 `json:"constraints" yaml:"constraints"`
	ProvenanceRef  string                  `json:"provenance_ref" yaml:"provenance_ref"`
}

type DelegationConstraintsV2 struct {
	MaxRisk          string     `json:"max_risk,omitempty" yaml:"max_risk,omitempty"`
	AllowedMethodIDs []string   `json:"allowed_method_ids,omitempty" yaml:"allowed_method_ids,omitempty"`
	AllowedCheckIDs  []string   `json:"allowed_check_ids,omitempty" yaml:"allowed_check_ids,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
}

type MethodPackV2 struct {
	ID                string                  `json:"id" yaml:"id"`
	Priority          int32                   `json:"priority" yaml:"priority"`
	Match             MatchV2                 `json:"match" yaml:"match"`
	Methods           []string                `json:"methods" yaml:"methods"`
	Checks            []string                `json:"checks" yaml:"checks"`
	RiskFloor         string                  `json:"risk_floor" yaml:"risk_floor"`
	IndependentReview bool                    `json:"independent_review" yaml:"independent_review"`
	Publication       PublicationConstraintV2 `json:"publication" yaml:"publication"`
	DelegationID      string                  `json:"delegation_id,omitempty" yaml:"delegation_id,omitempty"`
}

type MatchV2 struct {
	Domains  []string `json:"domains,omitempty" yaml:"domains,omitempty"`
	Modules  []string `json:"modules,omitempty" yaml:"modules,omitempty"`
	Features []string `json:"features,omitempty" yaml:"features,omitempty"`
	Paths    []string `json:"paths,omitempty" yaml:"paths,omitempty"`
	Branches []string `json:"branches,omitempty" yaml:"branches,omitempty"`
	Authors  []string `json:"authors,omitempty" yaml:"authors,omitempty"`
	Labels   []string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

type PublicationConstraintV2 struct {
	Mode            string   `json:"mode" yaml:"mode"`
	ArtifactClasses []string `json:"artifact_classes" yaml:"artifact_classes"`
	ManualRequired  bool     `json:"manual_required" yaml:"manual_required"`
}

type QualityGateV2 struct {
	RuleIDs          []string `json:"rule_ids" yaml:"rule_ids"`
	RequiredCoverage []string `json:"required_coverage" yaml:"required_coverage"`
	MinSeverity      string   `json:"min_severity" yaml:"min_severity"`
	MinStrength      string   `json:"min_strength" yaml:"min_strength"`
	BaselineMode     string   `json:"baseline_mode" yaml:"baseline_mode"`
	ContextName      string   `json:"context_name" yaml:"context_name"`
}

type RetentionV2 struct {
	AuditDays                 int `json:"audit_days" yaml:"audit_days"`
	SourceDays                int `json:"source_days" yaml:"source_days"`
	UnresolvedReservationDays int `json:"unresolved_reservation_days" yaml:"unresolved_reservation_days"`
}
