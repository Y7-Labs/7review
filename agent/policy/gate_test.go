package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func gateFixture(t *testing.T) (EffectivePolicy, GateInput) {
	t.Helper()
	effective, err := Compile(validConfig(), CompileContext{ProjectID: "org/repo", RuntimeAllowed: []string{"repo.read"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	coverage := review.CoverageProjection{Complete: true, Checks: []review.Check{{
		ID: "correctness", MethodID: "builtin/correctness", Scope: "project", Required: true,
		ApplicabilityReason: "configured", Status: review.CheckSatisfied, EvidenceRefs: []string{"test:1"},
	}}}
	input := GateInput{
		Assessment: review.AssessmentProjection{
			ID: "assessment-1", Version: 1, Purpose: "assessment", SnapshotDigest: "sha256:snapshot",
			PolicyDigest: effective.Digest, Completeness: review.AssessmentComplete, Risk: "high", CreatedAt: now,
		},
		AssessmentDigest: "sha256:" + strings.Repeat("a", 64), Coverage: coverage, EvaluatedAt: now,
	}
	return effective, input
}

func TestQualityGateEvaluationPrecedenceAndExitCodes(t *testing.T) {
	effective, input := gateFixture(t)
	input.Findings = []GateFinding{{
		Key: "auth:1", RuleID: "auth", Severity: review.SeverityCritical, Strength: "confirmed",
		EvidenceRefs: []string{"finding:auth:1"}, InChangedScope: true, Validated: true,
	}}
	result, err := EvaluateQualityGate(effective, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != review.GateViolations || GateExitCode(result) != 0 {
		t.Fatalf("advisory violation must remain visible without failing CI: %#v", result)
	}
	effective.GateMode = "blocking"
	result, err = EvaluateQualityGate(effective, input)
	if err != nil {
		t.Fatal(err)
	}
	if GateExitCode(result) != 1 {
		t.Fatalf("blocking violation must exit 1: %#v", result)
	}
	input.Coverage.Complete = false
	input.Coverage.UnknownCheckIDs = []string{"security"}
	input.Assessment.Completeness = review.AssessmentPartial
	input.Assessment.StopReason = "deadline"
	result, err = EvaluateQualityGate(effective, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != review.GateIncomplete || GateExitCode(result) != 2 || len(result.ViolatedRuleIDs) != 1 {
		t.Fatalf("incomplete must retain known violations and exit 2: %#v", result)
	}
}

func TestQualityGateTreatsRequiredCheckViolationAsViolation(t *testing.T) {
	effective, input := gateFixture(t)
	input.Coverage.Checks[0].Status = review.CheckViolated
	result, err := EvaluateQualityGate(effective, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != review.GateViolations || !contains(result.ViolatedRuleIDs, "correctness") {
		t.Fatalf("violated required check must violate the gate: %#v", result)
	}
}

func TestScenario_S50_QualityBaseline(t *testing.T) {
	effective, input := gateFixture(t)
	effective.QualityGate.BaselineMode = "new"
	input.Findings = []GateFinding{
		{Key: "existing", RuleID: "correctness", Severity: review.SeverityHigh, Strength: "confirmed", Validated: true},
		{Key: "new", RuleID: "correctness", Severity: review.SeverityHigh, Strength: "confirmed", Validated: true},
	}
	result, err := EvaluateQualityGate(effective, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != review.GateIncomplete || !contains(result.UnknownObligationIDs, "baseline") {
		t.Fatalf("missing baseline must be incomplete: %#v", result)
	}
	input.Baseline = &BaselineRef{
		AssessmentID: "baseline-1", PolicyDigest: effective.Digest, ContextName: effective.QualityGate.ContextName,
		RuleIDs: append([]string(nil), effective.QualityGate.RuleIDs...), CoverageIDs: append([]string(nil), effective.QualityGate.RequiredCoverage...),
		FindingKeys: []string{"existing"}, Verified: true,
	}
	result, err = EvaluateQualityGate(effective, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != review.GateViolations || len(result.ViolatedRuleIDs) != 1 || result.ViolatedRuleIDs[0] != "correctness" {
		t.Fatalf("compatible baseline must suppress only existing findings: %#v", result)
	}
	input.Baseline.PolicyDigest = "sha256:" + strings.Repeat("f", 64)
	result, err = EvaluateQualityGate(effective, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != review.GateIncomplete {
		t.Fatalf("incompatible baseline must not permit a pass: %#v", result)
	}
}
