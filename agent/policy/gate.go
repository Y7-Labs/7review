package policy

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

type GateFinding struct {
	Key            string
	RuleID         string
	Severity       review.Severity
	Strength       string
	EvidenceRefs   []string
	InChangedScope bool
	Validated      bool
}

type GateInput struct {
	Assessment       review.AssessmentProjection
	AssessmentDigest string
	Coverage         review.CoverageProjection
	Findings         []GateFinding
	Baseline         *BaselineRef
	EvaluatedAt      time.Time
}

func EvaluateQualityGate(effective EffectivePolicy, input GateInput) (review.GateResult, error) {
	result := review.GateResult{
		AssessmentID: input.Assessment.ID, AssessmentVersion: input.Assessment.Version,
		AssessmentDigest: input.AssessmentDigest, PolicyDigest: effective.Digest,
		Mode: review.GateMode(effective.GateMode), EvaluatedAt: input.EvaluatedAt,
	}
	if strings.TrimSpace(effective.Digest) == "" || strings.TrimSpace(input.AssessmentDigest) == "" || input.EvaluatedAt.IsZero() {
		return result, fmt.Errorf("policy: gate evaluation requires policy, assessment digest and evaluation time")
	}
	if input.Assessment.PolicyDigest != effective.Digest {
		return result, fmt.Errorf("policy: assessment was not produced under the effective policy")
	}
	if err := input.Assessment.Validate(input.Coverage); err != nil {
		return result, fmt.Errorf("policy: invalid gate assessment: %w", err)
	}

	unknown := missingRequiredCoverage(effective.QualityGate.RequiredCoverage, input.Coverage)
	baselineCompatible := true
	if err := ValidateBaseline(effective, input.Baseline); err != nil {
		baselineCompatible = false
		unknown = append(unknown, "baseline")
	}
	violations, evidence := matchingViolations(effective.QualityGate, input.Findings, input.Baseline, baselineCompatible)
	violations = append(violations, violatedRequiredCoverage(effective.QualityGate.RequiredCoverage, input.Coverage)...)
	violations = sortedUnique(violations)
	result.ViolatedRuleIDs = violations
	result.UnknownObligationIDs = sortedUnique(unknown)
	result.EvidenceRefs = evidence

	switch {
	case input.Assessment.Completeness != review.AssessmentComplete || !input.Coverage.Complete || len(result.UnknownObligationIDs) > 0:
		result.Outcome = review.GateIncomplete
	case len(result.ViolatedRuleIDs) > 0:
		result.Outcome = review.GateViolations
	default:
		result.Outcome = review.GatePass
	}
	if err := result.Validate(input.Coverage); err != nil {
		return review.GateResult{}, fmt.Errorf("policy: invalid gate result: %w", err)
	}
	return result, nil
}

func GateExitCode(result review.GateResult) int {
	if result.Outcome == review.GatePass {
		return 0
	}
	if result.Outcome == review.GateIncomplete || result.Outcome == review.GateError {
		return 2
	}
	if result.Mode == review.GateBlocking && result.Outcome == review.GateViolations {
		return 1
	}
	if result.Mode == review.GateAdvisory && result.Outcome == review.GateViolations {
		return 0
	}
	return 2
}

func missingRequiredCoverage(required []string, coverage review.CoverageProjection) []string {
	checks := make(map[string]review.CheckStatus, len(coverage.Checks))
	for _, check := range coverage.Checks {
		checks[check.ID] = check.Status
	}
	unknown := append([]string(nil), coverage.UnknownCheckIDs...)
	for _, id := range required {
		status, exists := checks[id]
		if !exists || status == review.CheckPending || status == review.CheckRunning || status == review.CheckUnknown {
			unknown = append(unknown, id)
		}
	}
	return unknown
}

func violatedRequiredCoverage(required []string, coverage review.CoverageProjection) []string {
	requiredSet := toSet(required)
	var violated []string
	for _, check := range coverage.Checks {
		if requiredSet[check.ID] && check.Status == review.CheckViolated {
			violated = append(violated, check.ID)
		}
	}
	return violated
}

func matchingViolations(gate QualityGateV2, findings []GateFinding, baseline *BaselineRef, baselineCompatible bool) ([]string, []string) {
	rules := toSet(gate.RuleIDs)
	baselineKeys := map[string]bool{}
	if baselineCompatible && baseline != nil {
		baselineKeys = toSet(baseline.FindingKeys)
	}
	var violations, evidence []string
	for _, finding := range findings {
		if !finding.Validated || finding.Key == "" || finding.RuleID == "" {
			continue
		}
		if len(rules) > 0 && !rules[finding.RuleID] {
			continue
		}
		if severityRank(string(finding.Severity)) < severityRank(gate.MinSeverity) || strengthRank(finding.Strength) < strengthRank(gate.MinStrength) {
			continue
		}
		if gate.BaselineMode != "all" && baselineKeys[finding.Key] {
			continue
		}
		if gate.BaselineMode == "changed" && !finding.InChangedScope {
			continue
		}
		violations = append(violations, finding.RuleID)
		evidence = append(evidence, finding.EvidenceRefs...)
	}
	return sortedUnique(violations), sortedUnique(evidence)
}

func severityRank(value string) int {
	return map[string]int{"info": 1, "low": 2, "medium": 3, "high": 4, "critical": 5}[value]
}

func strengthRank(value string) int {
	return map[string]int{"note": 1, "human_check": 2, "confirmed": 3}[value]
}

func sortedUnique(values []string) []string {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = true
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
