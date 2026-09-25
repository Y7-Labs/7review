package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type CompileContext struct {
	ProjectID       string
	ChangedPaths    []string
	Branch          string
	Author          string
	Labels          []string
	RuntimeAllowed  []string
	RuntimeCeilings *LimitsV2
	EvaluationTime  time.Time
}

type EffectivePolicy struct {
	SchemaVersion     int
	Digest            string
	Methods           []string
	RequiredChecks    []string
	RiskFloor         string
	IndependentReview bool
	Publication       PublicationConstraintV2
	GateMode          string
	QualityGate       QualityGateV2
	AllowedTools      []string
	RequiredTools     []string
	Limits            LimitsV2
	MatchedPacks      []string
	Explanation       []ExplanationEntry
}

type ExplanationEntry struct {
	Field      string `json:"field"`
	Value      string `json:"value"`
	Source     string `json:"source"`
	Reason     string `json:"reason"`
	Precedence int32  `json:"precedence"`
}

func Compile(config ReviewConfigV2, ctx CompileContext) (EffectivePolicy, error) {
	if err := Validate(config); err != nil {
		return EffectivePolicy{}, err
	}
	if config.ProjectID != "" && ctx.ProjectID != "" && config.ProjectID != ctx.ProjectID {
		return EffectivePolicy{}, fmt.Errorf("policy: authenticated project %q does not match policy project %q", ctx.ProjectID, config.ProjectID)
	}
	if err := enforceRuntimeCeilings(config.Limits, ctx.RuntimeCeilings); err != nil {
		return EffectivePolicy{}, err
	}
	runtime := toSet(ctx.RuntimeAllowed)
	allowed := make([]string, 0, len(config.Capabilities.Allowed))
	for _, capability := range config.Capabilities.Allowed {
		if len(runtime) == 0 || runtime[capability] {
			allowed = append(allowed, capability)
		}
	}
	for _, capability := range config.Capabilities.Required {
		if len(runtime) > 0 && !runtime[capability] {
			return EffectivePolicy{}, fmt.Errorf("policy: required capability %q is unavailable at runtime", capability)
		}
	}

	effective := EffectivePolicy{
		SchemaVersion:  2,
		Methods:        append([]string(nil), config.Defaults.Methods...),
		RequiredChecks: append([]string(nil), config.Defaults.RequiredChecks...),
		RiskFloor:      "none",
		Publication: PublicationConstraintV2{
			Mode:            config.Defaults.Publication,
			ArtifactClasses: []string{"assessment", "comment", "check", "annotation", "code_quality"},
		},
		GateMode:      config.Defaults.GateMode,
		QualityGate:   config.QualityGate,
		AllowedTools:  allowed,
		RequiredTools: append([]string(nil), config.Capabilities.Required...),
		Limits:        config.Limits,
		Explanation: []ExplanationEntry{
			{Field: "methods", Value: strings.Join(config.Defaults.Methods, ","), Source: "defaults", Reason: "trusted project defaults", Precedence: -1},
			{Field: "publication", Value: config.Defaults.Publication, Source: "defaults", Reason: "trusted project default", Precedence: -1},
		},
	}

	packs := append([]MethodPackV2(nil), config.Packs...)
	sort.Slice(packs, func(i, j int) bool {
		if packs[i].Priority != packs[j].Priority {
			return packs[i].Priority > packs[j].Priority
		}
		return packs[i].ID < packs[j].ID
	})
	delegations := make(map[string]DelegationV2, len(config.Delegations))
	for _, delegation := range config.Delegations {
		delegations[delegation.ID] = delegation
	}
	for _, pack := range packs {
		if !packMatches(pack.Match, config, ctx) {
			continue
		}
		if pack.DelegationID != "" {
			delegation := delegations[pack.DelegationID]
			if !scopeMatches(delegation.TargetScope, config, ctx) {
				continue
			}
			if !scopeMatches(delegation.GrantorScope, config, ctx) {
				return EffectivePolicy{}, fmt.Errorf("policy: delegation %q grantor is not authoritative for the matched change", delegation.ID)
			}
			if err := applyDelegatedReplacement(&effective, pack, delegation, ctx.EvaluationTime); err != nil {
				return EffectivePolicy{}, err
			}
		}
		effective.MatchedPacks = append(effective.MatchedPacks, pack.ID)
		effective.Methods = stableUnion(effective.Methods, pack.Methods)
		effective.RequiredChecks = stableUnion(effective.RequiredChecks, pack.Checks)
		if riskRank(pack.RiskFloor) > riskRank(effective.RiskFloor) {
			effective.RiskFloor = pack.RiskFloor
		}
		effective.IndependentReview = effective.IndependentReview || pack.IndependentReview
		effective.Publication = restrictivePublication(effective.Publication, pack.Publication)
		effective.Explanation = append(effective.Explanation,
			ExplanationEntry{Field: "pack", Value: pack.ID, Source: "packs/" + pack.ID, Reason: "all declared predicate families matched", Precedence: pack.Priority},
			ExplanationEntry{Field: "risk_floor", Value: effective.RiskFloor, Source: "packs/" + pack.ID, Reason: "maximum applicable risk floor", Precedence: pack.Priority},
		)
	}
	canonical, err := json.Marshal(struct {
		Config  ReviewConfigV2
		Context CompileContext
	}{config, CompileContext{ProjectID: ctx.ProjectID, ChangedPaths: sortedCopy(ctx.ChangedPaths), Branch: ctx.Branch, Author: ctx.Author, Labels: sortedCopy(ctx.Labels), RuntimeAllowed: sortedCopy(ctx.RuntimeAllowed)}})
	if err != nil {
		return EffectivePolicy{}, fmt.Errorf("policy: canonicalize: %w", err)
	}
	sum := sha256.Sum256(canonical)
	effective.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return effective, nil
}

func enforceRuntimeCeilings(requested LimitsV2, ceilings *LimitsV2) error {
	if ceilings == nil {
		return nil
	}
	for name, pair := range map[string][2]LimitScopeV2{"attempt": {requested.Attempt, ceilings.Attempt}, "change": {requested.Change, ceilings.Change}, "project": {requested.Project, ceilings.Project}} {
		r, c := pair[0], pair[1]
		values := map[string][2]int64{
			"model_calls": {r.ModelCalls, c.ModelCalls}, "tool_calls": {r.ToolCalls, c.ToolCalls},
			"input_tokens": {r.InputTokens, c.InputTokens}, "output_tokens": {r.OutputTokens, c.OutputTokens},
			"money_micro": {r.MoneyMicro, c.MoneyMicro}, "active_ms": {r.ActiveMS, c.ActiveMS},
			"observation_bytes": {r.ObservationBytes, c.ObservationBytes},
		}
		for resource, value := range values {
			if value[1] >= 0 && value[0] > value[1] {
				return fmt.Errorf("policy: limits.%s.%s exceeds operator ceiling", name, resource)
			}
		}
	}
	return nil
}

func applyDelegatedReplacement(e *EffectivePolicy, pack MethodPackV2, d DelegationV2, at time.Time) error {
	if d.Constraints.ExpiresAt != nil {
		if at.IsZero() {
			return fmt.Errorf("policy: delegation %q expiry requires an explicit compilation clock", d.ID)
		}
		if !at.Before(*d.Constraints.ExpiresAt) {
			return fmt.Errorf("policy: delegation %q is expired", d.ID)
		}
	}
	methodAllowed, checkAllowed := toSet(d.Constraints.AllowedMethodIDs), toSet(d.Constraints.AllowedCheckIDs)
	for _, method := range pack.Methods {
		if len(methodAllowed) > 0 && !methodAllowed[method] {
			return fmt.Errorf("policy: delegation %q does not allow method %q", d.ID, method)
		}
	}
	for _, check := range pack.Checks {
		if len(checkAllowed) > 0 && !checkAllowed[check] {
			return fmt.Errorf("policy: delegation %q does not allow check %q", d.ID, check)
		}
	}
	fields := toSet(d.ReplaceFields)
	remove := toSet(d.ReplaceRuleIDs)
	if fields["methods"] {
		e.Methods = without(e.Methods, remove)
	}
	if fields["checks"] {
		e.RequiredChecks = without(e.RequiredChecks, remove)
	}
	if fields["risk_floor"] {
		if d.Constraints.MaxRisk != "" && riskRank(pack.RiskFloor) > riskRank(d.Constraints.MaxRisk) {
			return fmt.Errorf("policy: delegation %q risk floor exceeds its constraint", d.ID)
		}
		e.RiskFloor = pack.RiskFloor
	}
	if fields["independent_review"] {
		e.IndependentReview = pack.IndependentReview
	}
	if fields["publication"] {
		if pack.Publication.Mode != "inherit" {
			e.Publication = pack.Publication
		}
	}
	e.Explanation = append(e.Explanation, ExplanationEntry{Field: "delegation", Value: d.ID, Source: d.ProvenanceRef, Reason: "authorized replacement applied before pack union", Precedence: pack.Priority})
	return nil
}

func packMatches(m MatchV2, c ReviewConfigV2, ctx CompileContext) bool {
	families := []bool{}
	if m.Domains != nil {
		families = append(families, anyScopeMapping(m.Domains, c.Domains, ctx.ChangedPaths))
	}
	if m.Modules != nil {
		families = append(families, anyScopeMapping(m.Modules, c.Modules, ctx.ChangedPaths))
	}
	if m.Features != nil {
		families = append(families, anyScopeMapping(m.Features, c.Features, ctx.ChangedPaths))
	}
	if m.Paths != nil {
		families = append(families, anyGlob(m.Paths, ctx.ChangedPaths))
	}
	if m.Branches != nil {
		families = append(families, anyGlob(m.Branches, []string{ctx.Branch}))
	}
	if m.Authors != nil {
		families = append(families, contains(m.Authors, ctx.Author))
	}
	if m.Labels != nil {
		families = append(families, intersects(m.Labels, ctx.Labels))
	}
	for _, matched := range families {
		if !matched {
			return false
		}
	}
	return true
}

func scopeMatches(scope ScopeRefV2, c ReviewConfigV2, ctx CompileContext) bool {
	switch scope.Kind {
	case "project":
		return c.ProjectID == "" || scope.ID == c.ProjectID
	case "domain":
		return anyGlob(c.Domains[scope.ID], ctx.ChangedPaths)
	case "module":
		return anyGlob(c.Modules[scope.ID], ctx.ChangedPaths)
	case "feature":
		return anyGlob(c.Features[scope.ID], ctx.ChangedPaths)
	case "path":
		return anyGlob([]string{scope.ID}, ctx.ChangedPaths)
	default:
		return false
	}
}

func anyScopeMapping(ids []string, mappings map[string][]string, paths []string) bool {
	for _, id := range ids {
		if anyGlob(mappings[id], paths) {
			return true
		}
	}
	return false
}

func anyGlob(patterns, values []string) bool {
	for _, pattern := range patterns {
		re, err := globRegexp(pattern)
		if err != nil {
			continue
		}
		for _, value := range values {
			if re.MatchString(value) {
				return true
			}
		}
	}
	return false
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func stableUnion(base []string, additions ...[]string) []string {
	out := append([]string(nil), base...)
	seen := toSet(out)
	for _, values := range additions {
		for _, value := range values {
			if !seen[value] {
				seen[value] = true
				out = append(out, value)
			}
		}
	}
	return out
}

func without(values []string, removed map[string]bool) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !removed[value] {
			out = append(out, value)
		}
	}
	return out
}

func restrictivePublication(current, next PublicationConstraintV2) PublicationConstraintV2 {
	rank := map[string]int{"setup_grant": 1, "human_authorized": 2, "none": 3}
	if next.Mode != "inherit" && rank[next.Mode] > rank[current.Mode] {
		current.Mode = next.Mode
	}
	current.ManualRequired = current.ManualRequired || next.ManualRequired
	if len(next.ArtifactClasses) > 0 {
		allowed := toSet(next.ArtifactClasses)
		filtered := current.ArtifactClasses[:0]
		for _, class := range current.ArtifactClasses {
			if allowed[class] {
				filtered = append(filtered, class)
			}
		}
		current.ArtifactClasses = filtered
	}
	return current
}

func riskRank(value string) int {
	return map[string]int{"none": 0, "low": 1, "medium": 2, "high": 3, "critical": 4}[value]
}
func contains(values []string, wanted string) bool { return toSet(values)[wanted] }
func intersects(a, b []string) bool {
	set := toSet(a)
	for _, value := range b {
		if set[value] {
			return true
		}
	}
	return false
}
func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
