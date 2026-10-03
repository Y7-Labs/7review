package pipeline

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

func EvaluateImportedCIGate(
	effective policy.EffectivePolicy,
	assessment review.AssessmentProjection,
	assessmentDigest string,
	evidence []review.ImportedCIQualityEvidence,
	baseline *policy.BaselineRef,
	evaluatedAt time.Time,
) (review.CoverageProjection, review.GateResult, error) {
	coverage, findings, err := buildCIGateInputs(effective, evidence)
	if err != nil {
		return review.CoverageProjection{}, review.GateResult{}, err
	}
	if !coverage.Complete && assessment.Completeness == review.AssessmentComplete {
		assessment.Completeness = review.AssessmentPartial
		assessment.StopReason = "required CI evidence is unavailable"
	}
	gate, err := policy.EvaluateQualityGate(effective, policy.GateInput{
		Assessment: assessment, AssessmentDigest: assessmentDigest,
		Coverage: coverage, Findings: findings, Baseline: baseline, EvaluatedAt: evaluatedAt,
	})
	if err != nil {
		return coverage, review.GateResult{}, err
	}
	return coverage, gate, nil
}

func buildCIGateInputs(effective policy.EffectivePolicy, evidence []review.ImportedCIQualityEvidence) (review.CoverageProjection, []policy.GateFinding, error) {
	required := make(map[string]bool, len(effective.QualityGate.RequiredCoverage))
	for _, id := range effective.QualityGate.RequiredCoverage {
		required[id] = true
	}
	checks := make(map[string]review.Check, len(evidence))
	var findings []policy.GateFinding
	for _, imported := range evidence {
		if err := imported.Validate(); err != nil {
			return review.CoverageProjection{}, nil, fmt.Errorf("pipeline: imported CI evidence: %w", err)
		}
		check := imported.Check
		if _, exists := checks[check.ID]; exists {
			return review.CoverageProjection{}, nil, fmt.Errorf("pipeline: duplicate CI check %q", check.ID)
		}
		check.Required = required[check.ID]
		checks[check.ID] = check
		for _, signal := range imported.Signals {
			findings = append(findings, policy.GateFinding{
				Key: signal.Key, RuleID: signal.RuleID, Severity: signal.Severity,
				Strength: signal.Strength, EvidenceRefs: append([]string(nil), check.EvidenceRefs...),
				InChangedScope: signal.InChangedScope, Validated: true,
			})
		}
	}

	unknown := make([]string, 0, len(required))
	for id := range required {
		check, exists := checks[id]
		if !exists || check.Status == review.CheckPending || check.Status == review.CheckRunning || check.Status == review.CheckUnknown || check.Status == review.CheckNotApplicable {
			unknown = append(unknown, id)
		}
	}
	checkIDs := make([]string, 0, len(checks))
	for id := range checks {
		checkIDs = append(checkIDs, id)
	}
	sort.Strings(checkIDs)
	sort.Strings(unknown)
	coverage := review.CoverageProjection{Complete: len(unknown) == 0, UnknownCheckIDs: unknown}
	for _, id := range checkIDs {
		coverage.Checks = append(coverage.Checks, checks[id])
	}
	return coverage, findings, nil
}

func ImportedCIGateSummary(coverage review.CoverageProjection, gate review.GateResult) string {
	return fmt.Sprintf(
		"outcome=%s mode=%s checks=%d violations=%s unknown=%s exit=%d",
		gate.Outcome, gate.Mode, len(coverage.Checks), strings.Join(gate.ViolatedRuleIDs, ","),
		strings.Join(gate.UnknownObligationIDs, ","), policy.GateExitCode(gate),
	)
}
