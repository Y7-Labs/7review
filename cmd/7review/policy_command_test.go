package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/policy"
)

func TestPolicyCommandValidateAndExplain(t *testing.T) {
	path := writePolicyFixture(t)
	var validated bytes.Buffer
	if err := runPolicyCommand([]string{"validate", "--file", path}, &validated); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(validated.String(), `"valid": true`) {
		t.Fatalf("unexpected validation output: %s", validated.String())
	}
	var explained bytes.Buffer
	err := runPolicyCommand([]string{"explain", "--file", path, "--project", "org/repo", "--path", "backend/auth.go", "--capability", "repo.read"}, &explained)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"trusted_source": "unbound_preview"`, `"policy_digest": "sha256:`, `"matched_packs"`} {
		if !strings.Contains(explained.String(), expected) {
			t.Fatalf("explanation missing %q: %s", expected, explained.String())
		}
	}
}

func TestPolicyCommandRejectsMissingFileAndUnknownFlag(t *testing.T) {
	if err := runPolicyCommand([]string{"validate"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--file") {
		t.Fatalf("expected missing file error, got %v", err)
	}
	if err := runPolicyCommand([]string{"validate", "--wat", "x"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("expected unknown flag error, got %v", err)
	}
}

func writePolicyFixture(t *testing.T) string {
	t.Helper()
	period := &policy.FixedPeriodV2{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 86_400_000}
	limit := policy.LimitScopeV2{ModelCalls: 2, ToolCalls: 4, InputTokens: 100, OutputTokens: 20, ActiveMS: 1000, ObservationBytes: 4096}
	change, project := limit, limit
	change.Period, project.Period = period, period
	config := policy.ReviewConfigV2{
		SchemaVersion: 2, ProjectID: "org/repo",
		Defaults:     policy.DefaultsV2{Methods: []string{"builtin/correctness"}, Publication: "human_authorized", GateMode: "advisory", RequiredChecks: []string{"correctness"}},
		Limits:       policy.LimitsV2{Attempt: limit, Change: change, Project: project},
		Triggers:     policy.TriggersV2{Enabled: true, OnUpdates: true, IncludeBranches: []string{}, ExcludeBranches: []string{}, IncludeAuthors: []string{}, ExcludeAuthors: []string{}, IncludeLabels: []string{}, ExcludeLabels: []string{}},
		Capabilities: policy.CapabilitiesV2{Allowed: []string{"repo.read"}, Required: []string{"repo.read"}},
		Domains:      map[string][]string{}, Modules: map[string][]string{}, Features: map[string][]string{}, Delegations: []policy.DelegationV2{}, Packs: []policy.MethodPackV2{},
		QualityGate: policy.QualityGateV2{RuleIDs: []string{}, RequiredCoverage: []string{"correctness"}, MinSeverity: "high", MinStrength: "confirmed", BaselineMode: "all", ContextName: "7review/quality"},
		Retention:   policy.RetentionV2{AuditDays: 30, SourceDays: 7, UnresolvedReservationDays: 30},
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
