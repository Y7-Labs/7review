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

func EvaluateReviewAndImportedCIGate(
	effective policy.EffectivePolicy,
	assessment review.AssessmentProjection,
	assessmentDigest string,
	source review.Source,
	evidence []review.ImportedCIQualityEvidence,
	baseline *policy.BaselineRef,
	evaluatedAt time.Time,
) (review.CoverageProjection, review.GateResult, error) {
	coverage, findings, err := buildCIGateInputs(effective, evidence)
	if err != nil {
		return review.CoverageProjection{}, review.GateResult{}, err
	}
	coverage = mergeReviewCoverage(effective, coverage, source)
	findings = append(findings, reviewGateFindings(source)...)
	if !coverage.Complete && assessment.Completeness == review.AssessmentComplete {
		assessment.Completeness = review.AssessmentPartial
		assessment.StopReason = "required review or CI evidence is unavailable"
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

func mergeReviewCoverage(effective policy.EffectivePolicy, coverage review.CoverageProjection, source review.Source) review.CoverageProjection {
	required := make(map[string]bool, len(effective.QualityGate.RequiredCoverage))
	for _, id := range effective.QualityGate.RequiredCoverage {
		required[id] = true
	}
	checks := make(map[string]review.Check, len(coverage.Checks))
	for _, check := range coverage.Checks {
		checks[check.ID] = check
	}
	coveredSkills := make(map[string]review.SkillCoverage, len(source.SkillCoverage))
	for _, item := range source.SkillCoverage {
		coveredSkills[strings.ToLower(strings.TrimSpace(item.Name))] = item
	}
	for _, activation := range source.SkillActivations {
		item, exists := coveredSkills[strings.ToLower(strings.TrimSpace(activation.Name))]
		for _, checkID := range activation.RequiredChecks {
			candidate := review.Check{
				ID: checkID, MethodID: activation.Name, Scope: firstNonEmptyPipeline(activation.ReviewDomain, "project"),
				Required: required[checkID], ApplicabilityReason: activation.Reason,
			}
			if exists && skillCoverageIsMeaningful(item) && containsStringFold(item.Checks, checkID) {
				candidate.Status = review.CheckSatisfied
				candidate.EvidenceRefs = append([]string(nil), item.Evidence...)
				if len(candidate.EvidenceRefs) == 0 {
					candidate.EvidenceRefs = []string{"review-skill:" + activation.Name}
				}
			} else {
				candidate.Status = review.CheckUnknown
				candidate.StatusReason = "active review method did not establish auditable coverage"
			}
			checks[checkID] = mergeGateCheck(checks[checkID], candidate)
		}
	}
	ids := make([]string, 0, len(checks))
	for id := range checks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	coverage.Checks = coverage.Checks[:0]
	unknown := map[string]bool{}
	for _, id := range ids {
		check := checks[id]
		coverage.Checks = append(coverage.Checks, check)
		if required[id] && (check.Status == review.CheckPending || check.Status == review.CheckRunning || check.Status == review.CheckUnknown || check.Status == review.CheckNotApplicable) {
			unknown[id] = true
		}
	}
	for id := range required {
		if _, exists := checks[id]; !exists {
			unknown[id] = true
		}
	}
	coverage.UnknownCheckIDs = coverage.UnknownCheckIDs[:0]
	for id := range unknown {
		coverage.UnknownCheckIDs = append(coverage.UnknownCheckIDs, id)
	}
	sort.Strings(coverage.UnknownCheckIDs)
	coverage.Complete = len(coverage.UnknownCheckIDs) == 0
	return coverage
}

func mergeGateCheck(existing, candidate review.Check) review.Check {
	if existing.ID == "" {
		return candidate
	}
	rank := map[review.CheckStatus]int{
		review.CheckNotApplicable: 1, review.CheckSatisfied: 2, review.CheckPending: 3,
		review.CheckRunning: 3, review.CheckUnknown: 4, review.CheckViolated: 5,
	}
	if rank[candidate.Status] > rank[existing.Status] {
		candidate.EvidenceRefs = uniqueSortedStrings(append(candidate.EvidenceRefs, existing.EvidenceRefs...))
		candidate.Required = candidate.Required || existing.Required
		return candidate
	}
	existing.EvidenceRefs = uniqueSortedStrings(append(existing.EvidenceRefs, candidate.EvidenceRefs...))
	existing.Required = existing.Required || candidate.Required
	return existing
}

func reviewGateFindings(source review.Source) []policy.GateFinding {
	changed := map[string]bool{}
	for _, file := range source.ChangedFiles {
		changed[firstNonEmptyPipeline(file.NewPath, file.OldPath)] = true
	}
	findings := make([]policy.GateFinding, 0, len(source.Findings))
	for _, finding := range source.Findings {
		ruleID := "review-finding"
		evidence := []string{"review-finding:" + finding.ID}
		if len(finding.Citations) > 0 {
			if strings.TrimSpace(finding.Citations[0].Rule) != "" {
				ruleID = finding.Citations[0].Rule
			}
			evidence = evidence[:0]
			for _, citation := range finding.Citations {
				evidence = append(evidence, citation.Source+":"+citation.HeadingOrKey)
			}
		} else if strings.TrimSpace(finding.FindingType) != "" {
			ruleID = finding.FindingType
		}
		strength := finding.Strength
		if strength == "likely" {
			strength = "human_check"
		}
		findings = append(findings, policy.GateFinding{
			Key: finding.ID, RuleID: ruleID, Severity: finding.Severity, Strength: strength,
			EvidenceRefs: uniqueSortedStrings(evidence), InChangedScope: changed[finding.Location.Path], Validated: finding.ValidationStatus == "accepted",
		})
	}
	return findings
}

func uniqueSortedStrings(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			set[value] = true
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
