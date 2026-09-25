package policy

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
)

func Validate(c ReviewConfigV2) error {
	if c.SchemaVersion != 2 {
		return fmt.Errorf("policy: schema_version must be 2")
	}
	if c.ProjectID != "" && !validID(c.ProjectID) {
		return fmt.Errorf("policy: invalid project_id %q", c.ProjectID)
	}
	if err := uniqueIDs("defaults.methods", c.Defaults.Methods, 1, 32); err != nil {
		return err
	}
	if err := enum("defaults.publication", c.Defaults.Publication, "none", "human_authorized", "setup_grant"); err != nil {
		return err
	}
	if err := enum("defaults.gate_mode", c.Defaults.GateMode, "advisory", "blocking"); err != nil {
		return err
	}
	if err := uniqueIDs("defaults.required_checks", c.Defaults.RequiredChecks, 0, 128); err != nil {
		return err
	}
	if err := validateLimits(c.Limits); err != nil {
		return err
	}
	if err := validateTriggers(c.Triggers); err != nil {
		return err
	}
	if err := uniqueIDs("capabilities.allowed", c.Capabilities.Allowed, 0, 128); err != nil {
		return err
	}
	if err := uniqueIDs("capabilities.required", c.Capabilities.Required, 0, 128); err != nil {
		return err
	}
	allowed := toSet(c.Capabilities.Allowed)
	for _, capability := range c.Capabilities.Required {
		if !allowed[capability] {
			return fmt.Errorf("policy: required capability %q is not allowed", capability)
		}
	}
	for name, mappings := range map[string]map[string][]string{"domains": c.Domains, "modules": c.Modules, "features": c.Features} {
		if err := validateMappings(name, mappings); err != nil {
			return err
		}
	}
	delegations := make(map[string]DelegationV2, len(c.Delegations))
	if len(c.Delegations) > 256 {
		return fmt.Errorf("policy: delegations exceeds maximum 256")
	}
	for i, delegation := range c.Delegations {
		if err := validateDelegation(i, delegation, c); err != nil {
			return err
		}
		if _, exists := delegations[delegation.ID]; exists {
			return fmt.Errorf("policy: duplicate delegation id %q", delegation.ID)
		}
		delegations[delegation.ID] = delegation
	}
	if len(c.Packs) > 256 {
		return fmt.Errorf("policy: packs exceeds maximum 256")
	}
	packIDs := map[string]bool{}
	for i, pack := range c.Packs {
		if err := validatePack(i, pack, c, delegations); err != nil {
			return err
		}
		if packIDs[pack.ID] {
			return fmt.Errorf("policy: duplicate pack id %q", pack.ID)
		}
		packIDs[pack.ID] = true
	}
	if err := validateQualityGate(c.QualityGate, c.Defaults.GateMode); err != nil {
		return err
	}
	if c.Retention.AuditDays <= 0 || c.Retention.SourceDays <= 0 || c.Retention.UnresolvedReservationDays <= 0 {
		return fmt.Errorf("policy: retention periods must be positive")
	}
	if c.Retention.UnresolvedReservationDays < c.Retention.AuditDays {
		return fmt.Errorf("policy: unresolved reservations cannot expire before audit reconciliation")
	}
	return nil
}

func validateLimits(l LimitsV2) error {
	if l.Attempt.Period != nil {
		return fmt.Errorf("policy: limits.attempt.period is forbidden")
	}
	for name, scope := range map[string]LimitScopeV2{"attempt": l.Attempt, "change": l.Change, "project": l.Project} {
		values := []int64{scope.ModelCalls, scope.ToolCalls, scope.InputTokens, scope.OutputTokens, scope.MoneyMicro, scope.ActiveMS, scope.ObservationBytes}
		for _, value := range values {
			if value < 0 {
				return fmt.Errorf("policy: limits.%s values must be nonnegative", name)
			}
		}
		if name != "attempt" {
			if scope.Period == nil || scope.Period.Kind != "fixed" || scope.Period.Anchor.IsZero() || scope.Period.DurationMS < 1 || scope.Period.DurationMS > 31_536_000_000 {
				return fmt.Errorf("policy: limits.%s requires a valid fixed period", name)
			}
			if scope.Period.Anchor.Location() != time.UTC {
				return fmt.Errorf("policy: limits.%s period anchor must be UTC", name)
			}
		}
	}
	if l.Attempt.MoneyMicro > 0 || l.Change.MoneyMicro > 0 || l.Project.MoneyMicro > 0 {
		if !currencyPattern.MatchString(l.Currency) {
			return fmt.Errorf("policy: limits.currency must be an uppercase ISO-4217 code when money is enabled")
		}
	}
	return nil
}

func validateTriggers(t TriggersV2) error {
	for name, values := range map[string][]string{
		"include_branches": t.IncludeBranches, "exclude_branches": t.ExcludeBranches,
		"include_authors": t.IncludeAuthors, "exclude_authors": t.ExcludeAuthors,
		"include_labels": t.IncludeLabels, "exclude_labels": t.ExcludeLabels,
	} {
		if len(values) > 128 {
			return fmt.Errorf("policy: triggers.%s exceeds maximum 128", name)
		}
		if err := uniqueStrings("triggers."+name, values, true); err != nil {
			return err
		}
		for _, value := range values {
			if len(value) > 256 {
				return fmt.Errorf("policy: triggers.%s value exceeds 256 bytes", name)
			}
		}
	}
	return nil
}

func validateMappings(name string, mappings map[string][]string) error {
	if len(mappings) > 256 {
		return fmt.Errorf("policy: %s exceeds maximum 256", name)
	}
	for id, globs := range mappings {
		if !validID(id) {
			return fmt.Errorf("policy: %s has invalid id %q", name, id)
		}
		if len(globs) == 0 || len(globs) > 128 {
			return fmt.Errorf("policy: %s.%s requires 1-128 path globs", name, id)
		}
		if err := validateGlobs(name+"."+id, globs); err != nil {
			return err
		}
	}
	return nil
}

func validateDelegation(index int, d DelegationV2, c ReviewConfigV2) error {
	prefix := fmt.Sprintf("delegations[%d]", index)
	if !validID(d.ID) || strings.TrimSpace(d.ProvenanceRef) == "" {
		return fmt.Errorf("policy: %s requires valid id and provenance_ref", prefix)
	}
	if err := validateScope(prefix+".grantor_scope", d.GrantorScope, c); err != nil {
		return err
	}
	if err := validateScope(prefix+".target_scope", d.TargetScope, c); err != nil {
		return err
	}
	if len(d.ReplaceRuleIDs) == 0 && len(d.ReplaceFields) == 0 {
		return fmt.Errorf("policy: %s requires a replacement", prefix)
	}
	if err := uniqueIDs(prefix+".replace_rule_ids", d.ReplaceRuleIDs, 0, 128); err != nil {
		return err
	}
	if err := uniqueEnum(prefix+".replace_fields", d.ReplaceFields, 16, "methods", "checks", "risk_floor", "independent_review", "publication"); err != nil {
		return err
	}
	if d.Constraints.MaxRisk != "" {
		if err := enum(prefix+".constraints.max_risk", d.Constraints.MaxRisk, "low", "medium", "high", "critical"); err != nil {
			return err
		}
	}
	if err := uniqueIDs(prefix+".constraints.allowed_method_ids", d.Constraints.AllowedMethodIDs, 0, 128); err != nil {
		return err
	}
	return uniqueIDs(prefix+".constraints.allowed_check_ids", d.Constraints.AllowedCheckIDs, 0, 128)
}

func validateScope(name string, scope ScopeRefV2, c ReviewConfigV2) error {
	if err := enum(name+".kind", scope.Kind, "project", "domain", "module", "feature", "path"); err != nil {
		return err
	}
	if scope.Kind == "path" {
		return validateGlobs(name+".id", []string{scope.ID})
	}
	if !validID(scope.ID) {
		return fmt.Errorf("policy: %s.id is invalid", name)
	}
	var exists bool
	switch scope.Kind {
	case "project":
		exists = scope.ID == c.ProjectID || c.ProjectID == ""
	case "domain":
		_, exists = c.Domains[scope.ID]
	case "module":
		_, exists = c.Modules[scope.ID]
	case "feature":
		_, exists = c.Features[scope.ID]
	}
	if !exists {
		return fmt.Errorf("policy: %s references unknown scope %q", name, scope.ID)
	}
	return nil
}

func validatePack(index int, p MethodPackV2, c ReviewConfigV2, delegations map[string]DelegationV2) error {
	prefix := fmt.Sprintf("packs[%d]", index)
	if !validID(p.ID) {
		return fmt.Errorf("policy: %s.id is invalid", prefix)
	}
	if err := uniqueIDs(prefix+".methods", p.Methods, 0, 32); err != nil {
		return err
	}
	if err := uniqueIDs(prefix+".checks", p.Checks, 0, 128); err != nil {
		return err
	}
	if err := enum(prefix+".risk_floor", p.RiskFloor, "none", "low", "medium", "high", "critical"); err != nil {
		return err
	}
	if len(p.Methods) == 0 && len(p.Checks) == 0 && p.RiskFloor == "none" {
		return fmt.Errorf("policy: %s has no effective obligation", prefix)
	}
	if err := validateMatch(prefix+".match", p.Match, c); err != nil {
		return err
	}
	if err := validatePublication(prefix+".publication", p.Publication, true); err != nil {
		return err
	}
	if p.DelegationID != "" {
		if _, exists := delegations[p.DelegationID]; !exists {
			return fmt.Errorf("policy: %s references unknown delegation %q", prefix, p.DelegationID)
		}
	}
	return nil
}

func validateMatch(name string, m MatchV2, c ReviewConfigV2) error {
	families := map[string][]string{"domains": m.Domains, "modules": m.Modules, "features": m.Features, "paths": m.Paths, "branches": m.Branches, "authors": m.Authors, "labels": m.Labels}
	for family, values := range families {
		if len(values) > 128 {
			return fmt.Errorf("policy: %s.%s exceeds maximum 128", name, family)
		}
		if err := uniqueStrings(name+"."+family, values, false); err != nil {
			return err
		}
	}
	for _, id := range m.Domains {
		if _, ok := c.Domains[id]; !ok {
			return fmt.Errorf("policy: %s references unknown domain %q", name, id)
		}
	}
	for _, id := range m.Modules {
		if _, ok := c.Modules[id]; !ok {
			return fmt.Errorf("policy: %s references unknown module %q", name, id)
		}
	}
	for _, id := range m.Features {
		if _, ok := c.Features[id]; !ok {
			return fmt.Errorf("policy: %s references unknown feature %q", name, id)
		}
	}
	return validateGlobs(name+".paths", m.Paths)
}

func validatePublication(name string, p PublicationConstraintV2, inherit bool) error {
	modes := []string{"none", "human_authorized", "setup_grant"}
	if inherit {
		modes = append(modes, "inherit")
	}
	if err := enum(name+".mode", p.Mode, modes...); err != nil {
		return err
	}
	return uniqueEnum(name+".artifact_classes", p.ArtifactClasses, 5, "assessment", "comment", "check", "annotation", "code_quality")
}

func validateQualityGate(g QualityGateV2, mode string) error {
	if err := uniqueIDs("quality_gate.rule_ids", g.RuleIDs, 0, 128); err != nil {
		return err
	}
	if err := uniqueIDs("quality_gate.required_coverage", g.RequiredCoverage, 0, 128); err != nil {
		return err
	}
	if mode == "blocking" && len(g.RequiredCoverage) == 0 {
		return fmt.Errorf("policy: blocking quality gate requires coverage")
	}
	if err := enum("quality_gate.min_severity", g.MinSeverity, "info", "low", "medium", "high", "critical"); err != nil {
		return err
	}
	if err := enum("quality_gate.min_strength", g.MinStrength, "confirmed", "human_check", "note"); err != nil {
		return err
	}
	if err := enum("quality_gate.baseline_mode", g.BaselineMode, "all", "new", "changed"); err != nil {
		return err
	}
	if len(g.ContextName) < 1 || len(g.ContextName) > 100 || strings.IndexFunc(g.ContextName, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return fmt.Errorf("policy: quality_gate.context_name must be 1-100 printable bytes")
	}
	return nil
}

func validateGlobs(name string, globs []string) error {
	for _, glob := range globs {
		if glob == "" || strings.HasPrefix(glob, "/") || strings.Contains(glob, "\\") {
			return fmt.Errorf("policy: %s contains invalid repository glob %q", name, glob)
		}
		for _, part := range strings.Split(glob, "/") {
			if part == ".." {
				return fmt.Errorf("policy: %s glob cannot escape repository", name)
			}
		}
		if _, err := path.Match(strings.ReplaceAll(glob, "**", "*"), "probe"); err != nil {
			return fmt.Errorf("policy: %s contains malformed glob %q", name, glob)
		}
	}
	return uniqueStrings(name, globs, false)
}

func validID(value string) bool { return len(value) <= 256 && idPattern.MatchString(value) }

func uniqueIDs(name string, values []string, min, max int) error {
	if len(values) < min || len(values) > max {
		return fmt.Errorf("policy: %s requires %d-%d values", name, min, max)
	}
	for _, value := range values {
		if !validID(value) {
			return fmt.Errorf("policy: %s contains invalid id %q", name, value)
		}
	}
	return uniqueStrings(name, values, false)
}

func uniqueStrings(name string, values []string, nonempty bool) error {
	seen := map[string]bool{}
	for _, value := range values {
		if nonempty && strings.TrimSpace(value) == "" {
			return fmt.Errorf("policy: %s contains an empty value", name)
		}
		if seen[value] {
			return fmt.Errorf("policy: %s contains duplicate %q", name, value)
		}
		seen[value] = true
	}
	return nil
}

func uniqueEnum(name string, values []string, max int, allowed ...string) error {
	if len(values) > max {
		return fmt.Errorf("policy: %s exceeds maximum %d", name, max)
	}
	if err := uniqueStrings(name, values, false); err != nil {
		return err
	}
	for _, value := range values {
		if err := enum(name, value, allowed...); err != nil {
			return err
		}
	}
	return nil
}

func enum(name, value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("policy: %s has unsupported value %q", name, value)
}

func toSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}
