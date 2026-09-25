package review

import (
	"strings"
	"testing"
	"time"
)

type contractCase struct {
	name     string
	clause   string
	scenario string
	run      func() error
	wantErr  string
}

func runContractCases(t *testing.T, cases []contractCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.clause+"_"+tc.scenario+"_"+tc.name, func(t *testing.T) {
			err := tc.run()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected contract error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestDomainContract_SCHEMA04_IdentityAndExecutionBoundaries(t *testing.T) {
	deadline := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	runContractCases(t, []contractCase{
		{
			name:     "scm change",
			clause:   "SCHEMA-04",
			scenario: "S13",
			run: func() error {
				return (ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "r-1", ChangeNumber: "42"}).Validate()
			},
		},
		{
			name:     "local comparison required",
			clause:   "SCHEMA-04",
			scenario: "S24",
			run: func() error {
				return (ChangeKey{Provider: "local", RepositoryID: "r-1", WorkspaceID: "workspace"}).Validate()
			},
			wantErr: "workspace and comparison",
		},
		{
			name:     "ephemeral persistence",
			clause:   "SCHEMA-04",
			scenario: "S45",
			run: func() error {
				return (ExecutionContext{
					Mode: ExecutionEphemeral, ProducerID: "ci/job", ComparisonTree: "sha256:tree",
					Persistence: PersistenceJobLocal, TrustedBoundaryID: "runner-1", DeadlineAt: deadline,
				}).Validate()
			},
		},
		{
			name:     "service cannot claim local durability",
			clause:   "SCHEMA-04",
			scenario: "S45",
			run: func() error {
				return (ExecutionContext{
					Mode: ExecutionService, ProducerID: "webhook", ComparisonTree: "sha256:tree",
					Persistence: PersistenceJobLocal, TrustedBoundaryID: "service-1", DeadlineAt: deadline,
				}).Validate()
			},
			wantErr: "durable service",
		},
	})
}

func TestDomainContract_SCHEMA04_SnapshotIsHeadOrLocal(t *testing.T) {
	base := SnapshotIdentity{RepositoryID: "r-1", BaseRevision: "base-sha", FileManifestDigest: "sha256:manifest"}
	runContractCases(t, []contractCase{
		{
			name: "head snapshot", clause: "SCHEMA-04", scenario: "S24",
			run: func() error {
				s := base
				s.HeadRevision = "head-sha"
				return s.Validate()
			},
		},
		{
			name: "local snapshot", clause: "SCHEMA-04", scenario: "S24",
			run: func() error {
				s := base
				s.LocalSnapshotDigest = "sha256:local"
				return s.Validate()
			},
		},
		{
			name: "ambiguous snapshot", clause: "SCHEMA-04", scenario: "S24",
			run: func() error {
				s := base
				s.HeadRevision = "head-sha"
				s.LocalSnapshotDigest = "sha256:local"
				return s.Validate()
			},
			wantErr: "exactly one",
		},
	})
}

func TestDomainContract_LOOP05_AttemptTerminalStates(t *testing.T) {
	for _, state := range []AttemptState{AttemptAssessed, AttemptIncomplete, AttemptFailed, AttemptCancelled, AttemptSuperseded} {
		if !state.Terminal() {
			t.Fatalf("%s must be terminal", state)
		}
	}
	for _, state := range []AttemptState{AttemptAccepted, AttemptReady, AttemptInvestigating, AttemptWaiting} {
		if state.Terminal() {
			t.Fatalf("%s must remain nonterminal", state)
		}
	}
}

func TestDomainContract_LOOP05_AttemptTransitionTable(t *testing.T) {
	allowed := [][2]AttemptState{
		{AttemptAccepted, AttemptReady},
		{AttemptReady, AttemptInvestigating},
		{AttemptInvestigating, AttemptReady},
		{AttemptInvestigating, AttemptWaiting},
		{AttemptWaiting, AttemptReady},
		{AttemptInvestigating, AttemptAssessed},
		{AttemptAccepted, AttemptCancelled},
		{AttemptWaiting, AttemptSuperseded},
	}
	for _, transition := range allowed {
		if !CanTransitionAttempt(transition[0], transition[1]) {
			t.Errorf("expected %s -> %s to be allowed", transition[0], transition[1])
		}
	}
	for _, transition := range [][2]AttemptState{
		{AttemptAccepted, AttemptAssessed},
		{AttemptReady, AttemptWaiting},
		{AttemptAssessed, AttemptReady},
		{AttemptFailed, AttemptFailed},
	} {
		if CanTransitionAttempt(transition[0], transition[1]) {
			t.Errorf("expected %s -> %s to be rejected", transition[0], transition[1])
		}
	}
}

func TestNewCanonicalContextValidatesIdentityAndInitializesInvestigation(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	attempt := AttemptIdentity{
		AttemptID: "attempt-1", WorkIntentID: "intent-1", IntentKind: IntentAutomatic,
		Change:       ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "owner/repo", ChangeNumber: "42"},
		Snapshot:     SnapshotIdentity{RepositoryID: "owner/repo", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:manifest"},
		PolicyDigest: "sha256:policy", Generation: 1, State: AttemptAccepted, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	execution := ExecutionContext{
		Mode: ExecutionService, ProducerID: "github/webhook", ComparisonTree: "head", Persistence: PersistenceDurableService,
		TrustedBoundaryID: "service", DeadlineAt: now.Add(time.Hour),
	}
	rc, err := NewCanonicalContext(Request{ProjectID: "owner/repo", ChangeID: "42"}, attempt, execution)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Source.Attempt.AttemptID != "attempt-1" || rc.Source.Investigation.State != AttemptAccepted || rc.Source.Investigation.Version != 1 {
		t.Fatalf("canonical context not initialized from attempt: %#v", rc.Source)
	}
	if _, err := NewCanonicalContext(Request{ProjectID: "other/repo"}, attempt, execution); err == nil {
		t.Fatal("repository identity mismatch was accepted")
	}
}

func TestDomainContract_SCHEMA04_EventsAndHumanDecisionsAreVersionBound(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	runContractCases(t, []contractCase{
		{
			name: "versioned event", clause: "SCHEMA-04", scenario: "S13",
			run: func() error {
				return (AttemptEvent{
					ID: "event-1", AttemptID: "attempt-1", Type: "answer_received", ActorPrincipal: "user-1",
					SubjectRevision: "head-1", PayloadDigest: "sha256:event", ExpectedVersion: 3, OccurredAt: now,
				}).Validate()
			},
		},
		{
			name: "event without expected version", clause: "SCHEMA-04", scenario: "S14",
			run: func() error {
				return (AttemptEvent{
					ID: "event-1", AttemptID: "attempt-1", Type: "answer_received", ActorPrincipal: "user-1",
					SubjectRevision: "head-1", PayloadDigest: "sha256:event", OccurredAt: now,
				}).Validate()
			},
			wantErr: "expected version",
		},
		{
			name: "versioned human decision", clause: "PUB-01", scenario: "S30",
			run: func() error {
				return (HumanDecision{
					ID: "decision-1", ActorPrincipal: "reviewer-1", ActorRole: "publisher",
					Action: "authorize_publication", SubjectID: "assessment-1", SubjectVersion: 2,
					SubjectRevision: "head-1", Reason: "reviewed evidence", DecidedAt: now,
				}).Validate()
			},
		},
	})
}

func TestDomainContract_SCHEMA05_CheckEvidenceGuards(t *testing.T) {
	base := Check{ID: "correctness", MethodID: "builtin/correctness", Scope: "project", Required: true, ApplicabilityReason: "default"}
	runContractCases(t, []contractCase{
		{
			name: "satisfied with evidence", clause: "SCHEMA-05", scenario: "S19",
			run: func() error {
				c := base
				c.Status = CheckSatisfied
				c.EvidenceRefs = []string{"obs-1"}
				return c.Validate()
			},
		},
		{
			name: "satisfied without evidence", clause: "SCHEMA-05", scenario: "S19",
			run: func() error {
				c := base
				c.Status = CheckSatisfied
				return c.Validate()
			},
			wantErr: "requires evidence",
		},
		{
			name: "unknown without reason", clause: "SCHEMA-05", scenario: "S19",
			run: func() error {
				c := base
				c.Status = CheckUnknown
				return c.Validate()
			},
			wantErr: "requires a reason",
		},
	})
}

func TestDomainContract_SCHEMA06_GateCannotPassIncompleteCoverage(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	gate := GateResult{
		AssessmentID: "assessment-1", AssessmentVersion: 1, AssessmentDigest: "sha256:assessment",
		PolicyDigest: "sha256:policy", Mode: GateBlocking, Outcome: GatePass, EvaluatedAt: now,
	}
	if err := gate.Validate(CoverageProjection{Complete: false, UnknownCheckIDs: []string{"tests"}}); err == nil {
		t.Fatal("gate pass accepted incomplete coverage")
	}
	if err := gate.Validate(CoverageProjection{Complete: true}); err != nil {
		t.Fatalf("complete coverage rejected: %v", err)
	}
}

func TestDomainContract_LOOP05_AssessmentCompletenessFollowsCoverage(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	assessment := AssessmentProjection{
		ID: "assessment-1", Version: 1, Purpose: "assessment", SnapshotDigest: "sha256:snapshot",
		PolicyDigest: "sha256:policy", Completeness: AssessmentComplete, Risk: "high", CreatedAt: now,
	}
	if err := assessment.Validate(CoverageProjection{Complete: false, UnknownCheckIDs: []string{"tests"}}); err == nil {
		t.Fatal("complete assessment accepted incomplete coverage")
	}
	assessment.Completeness = AssessmentPartial
	assessment.StopReason = "deadline"
	if err := assessment.Validate(CoverageProjection{Complete: false, UnknownCheckIDs: []string{"tests"}}); err != nil {
		t.Fatalf("valid partial assessment rejected: %v", err)
	}
}

func TestGateResultCloneDoesNotShareSlices(t *testing.T) {
	original := GateResult{
		ViolatedRuleIDs: []string{"rule-1"}, UnknownObligationIDs: []string{"check-1"}, EvidenceRefs: []string{"obs-1"},
	}
	clone := original.Clone()
	clone.ViolatedRuleIDs[0] = "changed"
	clone.UnknownObligationIDs[0] = "changed"
	clone.EvidenceRefs[0] = "changed"
	if original.ViolatedRuleIDs[0] != "rule-1" || original.UnknownObligationIDs[0] != "check-1" || original.EvidenceRefs[0] != "obs-1" {
		t.Fatalf("gate clone shares mutable slices: %#v", original)
	}
}

func TestSourceKeepsLifecycleProjectionsSeparate(t *testing.T) {
	source := Source{
		Investigation: InvestigationProjection{State: AttemptAssessed, Version: 4},
		Coverage:      CoverageProjection{Complete: true},
		Assessment:    AssessmentProjection{Completeness: AssessmentComplete},
		Gate:          GateResult{Outcome: GateViolations},
		Delivery:      DeliveryProjection{Records: []DeliveryRecord{{OperationID: "publish-1", State: DeliveryPendingAuthorization}}},
	}
	if source.Investigation.State != AttemptAssessed || source.Assessment.Completeness != AssessmentComplete || source.Gate.Outcome != GateViolations {
		t.Fatalf("investigation and gate projections collapsed: %#v", source)
	}
	if source.Delivery.Records[0].State != DeliveryPendingAuthorization || !source.Coverage.Complete {
		t.Fatalf("coverage and delivery projections collapsed: %#v", source)
	}
}
