package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/profile"
	"github.com/Y4NN777/7review/agent/review"
)

func validConfig() ReviewConfigV2 {
	period := &FixedPeriodV2{Kind: "fixed", Anchor: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: 86_400_000}
	limit := LimitScopeV2{ModelCalls: 10, ToolCalls: 20, InputTokens: 1000, OutputTokens: 200, MoneyMicro: 100, ActiveMS: 1000, ObservationBytes: 4096}
	change, project := limit, limit
	change.Period, project.Period = period, period
	return ReviewConfigV2{
		SchemaVersion: 2,
		ProjectID:     "org/repo",
		Defaults:      DefaultsV2{Methods: []string{"builtin/correctness"}, Publication: "human_authorized", GateMode: "advisory", RequiredChecks: []string{"correctness"}},
		Limits:        LimitsV2{Currency: "USD", Attempt: limit, Change: change, Project: project},
		Triggers:      TriggersV2{Enabled: true, OnUpdates: true, IncludeBranches: []string{}, ExcludeBranches: []string{}, IncludeAuthors: []string{}, ExcludeAuthors: []string{}, IncludeLabels: []string{}, ExcludeLabels: []string{}},
		Capabilities:  CapabilitiesV2{Allowed: []string{"repo.read", "model.review"}, Required: []string{"repo.read"}},
		Domains:       map[string][]string{"backend": {"backend/**"}}, Modules: map[string][]string{}, Features: map[string][]string{},
		QualityGate: QualityGateV2{RequiredCoverage: []string{"correctness"}, MinSeverity: "high", MinStrength: "confirmed", BaselineMode: "all", ContextName: "7review/quality"},
		Retention:   RetentionV2{AuditDays: 365, SourceDays: 30, UnresolvedReservationDays: 365},
	}
}

func TestCompileBoundRejectsHeadPolicyAndAcceptsTrustedBase(t *testing.T) {
	config := validConfig()
	attestation := review.SnapshotAttestation{
		Snapshot:   review.SnapshotIdentity{RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)},
		ProducerID: "github", ComparisonTree: "merge-tree", VerifiedAt: time.Now().UTC(), Verified: true,
	}
	source := Source{RepositoryID: "org/repo", Revision: "head", Digest: "sha256:" + strings.Repeat("b", 64), Trust: TrustBaseSnapshot}
	if _, err := CompileBound(config, source, attestation, CompileContext{ProjectID: "org/repo", RuntimeAllowed: []string{"repo.read"}}); err == nil || !strings.Contains(err.Error(), "attested base") {
		t.Fatalf("head policy must not become authority: %v", err)
	}
	source.Revision = "base"
	if _, err := CompileBound(config, source, attestation, CompileContext{ProjectID: "org/repo", RuntimeAllowed: []string{"repo.read"}}); err != nil {
		t.Fatal(err)
	}
}

func TestCompileResolvesApplicablePacksDeterministically(t *testing.T) {
	config := validConfig()
	config.Packs = []MethodPackV2{
		{ID: "low", Priority: 10, Match: MatchV2{Paths: []string{"backend/**"}}, Methods: []string{"repo/backend"}, RiskFloor: "medium", Publication: PublicationConstraintV2{Mode: "inherit", ArtifactClasses: []string{}}},
		{ID: "auth", Priority: 100, Match: MatchV2{Domains: []string{"backend"}, Labels: []string{"security"}}, Checks: []string{"auth-boundary"}, RiskFloor: "high", IndependentReview: true, Publication: PublicationConstraintV2{Mode: "human_authorized", ArtifactClasses: []string{"assessment", "check"}, ManualRequired: true}},
	}
	got, err := Compile(config, CompileContext{ProjectID: "org/repo", ChangedPaths: []string{"backend/auth/policy.go"}, Labels: []string{"security"}, RuntimeAllowed: []string{"repo.read", "model.review"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.MatchedPacks, ",") != "auth,low" {
		t.Fatalf("unexpected pack order: %v", got.MatchedPacks)
	}
	if got.RiskFloor != "high" || !got.IndependentReview || got.Publication.Mode != "human_authorized" || !got.Publication.ManualRequired {
		t.Fatalf("constraints not composed: %#v", got)
	}
	if strings.Join(got.Publication.ArtifactClasses, ",") != "assessment,check" {
		t.Fatalf("publication classes were not restricted: %v", got.Publication.ArtifactClasses)
	}
	if strings.Join(got.RequiredChecks, ",") != "correctness,auth-boundary" {
		t.Fatalf("checks not composed: %v", got.RequiredChecks)
	}
	if got.Digest == "" || len(got.Explanation) < 4 {
		t.Fatalf("compiled policy lacks digest or explanation: %#v", got)
	}
}

func TestCompileRejectsUnavailableRequiredCapabilityAndCeilingIncrease(t *testing.T) {
	config := validConfig()
	if _, err := Compile(config, CompileContext{ProjectID: "org/repo", RuntimeAllowed: []string{"model.review"}}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("expected unavailable capability, got %v", err)
	}
	ceilings := config.Limits
	ceilings.Attempt.ModelCalls = 2
	if _, err := Compile(config, CompileContext{ProjectID: "org/repo", RuntimeAllowed: []string{"repo.read"}, RuntimeCeilings: &ceilings}); err == nil || !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("expected ceiling rejection, got %v", err)
	}
}

func TestDecodeStrictJSONAndYAML(t *testing.T) {
	jsonInput := `{"schema_version":2,"unknown":true}`
	if _, err := DecodeJSON([]byte(jsonInput)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected strict JSON error, got %v", err)
	}
	yamlInput := "schema_version: 2\nunknown: true\n"
	if _, err := DecodeYAML([]byte(yamlInput)); err == nil || !strings.Contains(err.Error(), "field unknown") {
		t.Fatalf("expected strict YAML error, got %v", err)
	}
}

func TestDecodeRejectsMissingRequiredAndDuplicateJSONFields(t *testing.T) {
	if _, err := DecodeJSON([]byte(`{"schema_version":2}`)); err == nil || !strings.Contains(err.Error(), "required field") {
		t.Fatalf("expected required-field error, got %v", err)
	}
	if _, err := DecodeJSON([]byte(`{"schema_version":2,"schema_version":2}`)); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("expected duplicate-key error, got %v", err)
	}
}

func TestValidateRejectsContractViolations(t *testing.T) {
	tests := map[string]func(*ReviewConfigV2){
		"duplicate method":          func(c *ReviewConfigV2) { c.Defaults.Methods = []string{"builtin/a", "builtin/a"} },
		"money without currency":    func(c *ReviewConfigV2) { c.Limits.Currency = "" },
		"attempt period":            func(c *ReviewConfigV2) { c.Limits.Attempt.Period = c.Limits.Change.Period },
		"required not allowed":      func(c *ReviewConfigV2) { c.Capabilities.Required = []string{"network.write"} },
		"escaping glob":             func(c *ReviewConfigV2) { c.Domains["backend"] = []string{"../secret/**"} },
		"blocking without coverage": func(c *ReviewConfigV2) { c.Defaults.GateMode = "blocking"; c.QualityGate.RequiredCoverage = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			mutate(&config)
			if err := Validate(config); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestTranslateLegacyDoesNotInventV2Authority(t *testing.T) {
	legacy := TranslateLegacy(&profile.CompiledProfile{Name: "legacy", Publishing: profile.PublishingProfile{FinalRequiresHumanApproval: true}})
	if legacy.SourceVersion != 1 || !legacy.FinalRequiresHumanApproval || !legacy.MigrationRequiresAcknowledged || len(legacy.UnspecifiedV2Obligations) == 0 {
		t.Fatalf("legacy translation lost migration boundaries: %#v", legacy)
	}
}
