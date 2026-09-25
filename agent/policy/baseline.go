package policy

import "fmt"

type BaselineRef struct {
	AssessmentID string
	PolicyDigest string
	ContextName  string
	RuleIDs      []string
	CoverageIDs  []string
	Verified     bool
}

func ValidateBaseline(effective EffectivePolicy, baseline *BaselineRef) error {
	if effective.QualityGate.BaselineMode == "all" {
		return nil
	}
	if baseline == nil || !baseline.Verified {
		return fmt.Errorf("policy: %s baseline mode requires a verified baseline", effective.QualityGate.BaselineMode)
	}
	if baseline.AssessmentID == "" || baseline.PolicyDigest != effective.Digest || baseline.ContextName != effective.QualityGate.ContextName {
		return fmt.Errorf("policy: baseline identity is incompatible with the effective quality gate")
	}
	if !sameSet(baseline.RuleIDs, effective.QualityGate.RuleIDs) || !sameSet(baseline.CoverageIDs, effective.QualityGate.RequiredCoverage) {
		return fmt.Errorf("policy: baseline rules or coverage are incompatible with the effective quality gate")
	}
	return nil
}

func sameSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	set := toSet(left)
	for _, value := range right {
		if !set[value] {
			return false
		}
	}
	return true
}
