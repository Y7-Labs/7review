package review

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type WorkIntentKind string

const (
	IntentAutomatic  WorkIntentKind = "automatic"
	IntentManual     WorkIntentKind = "manual"
	IntentHistorical WorkIntentKind = "historical"
	IntentLocal      WorkIntentKind = "local"
)

type ChangeKey struct {
	Provider     string `json:"provider"`
	Host         string `json:"host"`
	RepositoryID string `json:"repository_id"`
	ChangeNumber string `json:"change_number,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	ComparisonID string `json:"comparison_id,omitempty"`
}

func (k ChangeKey) Validate() error {
	if strings.TrimSpace(k.Provider) == "" {
		return errors.New("change key provider is required")
	}
	if strings.TrimSpace(k.RepositoryID) == "" {
		return errors.New("change key repository ID is required")
	}
	if k.Provider == "local" {
		if strings.TrimSpace(k.WorkspaceID) == "" || strings.TrimSpace(k.ComparisonID) == "" {
			return errors.New("local change key requires workspace and comparison IDs")
		}
		return nil
	}
	if strings.TrimSpace(k.Host) == "" || strings.TrimSpace(k.ChangeNumber) == "" {
		return errors.New("SCM change key requires host and change number")
	}
	return nil
}

type SnapshotIdentity struct {
	RepositoryID        string `json:"repository_id"`
	BaseRevision        string `json:"base_revision,omitempty"`
	HeadRevision        string `json:"head_revision,omitempty"`
	LocalSnapshotDigest string `json:"local_snapshot_digest,omitempty"`
	FileManifestDigest  string `json:"file_manifest_digest"`
}

func (s SnapshotIdentity) Validate() error {
	if strings.TrimSpace(s.RepositoryID) == "" {
		return errors.New("snapshot repository ID is required")
	}
	if strings.TrimSpace(s.FileManifestDigest) == "" {
		return errors.New("snapshot file manifest digest is required")
	}
	if (strings.TrimSpace(s.HeadRevision) == "") == (strings.TrimSpace(s.LocalSnapshotDigest) == "") {
		return errors.New("snapshot requires exactly one of head revision or local digest")
	}
	if strings.TrimSpace(s.HeadRevision) != "" && strings.TrimSpace(s.BaseRevision) == "" {
		return errors.New("SCM snapshot requires a base revision")
	}
	return nil
}

type AttemptState string

const (
	AttemptAccepted      AttemptState = "accepted"
	AttemptReady         AttemptState = "ready"
	AttemptInvestigating AttemptState = "investigating"
	AttemptWaiting       AttemptState = "waiting"
	AttemptAssessed      AttemptState = "assessed"
	AttemptIncomplete    AttemptState = "incomplete"
	AttemptFailed        AttemptState = "failed"
	AttemptCancelled     AttemptState = "cancelled"
	AttemptSuperseded    AttemptState = "superseded"
)

func (s AttemptState) Terminal() bool {
	switch s {
	case AttemptAssessed, AttemptIncomplete, AttemptFailed, AttemptCancelled, AttemptSuperseded:
		return true
	default:
		return false
	}
}

func CanTransitionAttempt(from, to AttemptState) bool {
	if from.Terminal() || from == to {
		return false
	}
	switch to {
	case AttemptIncomplete, AttemptFailed, AttemptCancelled, AttemptSuperseded:
		return from == AttemptAccepted || from == AttemptReady || from == AttemptInvestigating || from == AttemptWaiting
	}
	switch from {
	case AttemptAccepted:
		return to == AttemptReady
	case AttemptReady:
		return to == AttemptInvestigating
	case AttemptInvestigating:
		return to == AttemptReady || to == AttemptWaiting || to == AttemptAssessed
	case AttemptWaiting:
		return to == AttemptReady
	default:
		return false
	}
}

type AttemptIdentity struct {
	AttemptID            string           `json:"attempt_id"`
	WorkIntentID         string           `json:"work_intent_id"`
	IntentKind           WorkIntentKind   `json:"intent_kind"`
	Change               ChangeKey        `json:"change"`
	Snapshot             SnapshotIdentity `json:"snapshot"`
	PolicyDigest         string           `json:"policy_digest"`
	Generation           uint64           `json:"generation"`
	PredecessorAttemptID string           `json:"predecessor_attempt_id,omitempty"`
	State                AttemptState     `json:"state"`
	Version              uint64           `json:"version"`
	CreatedAt            time.Time        `json:"created_at"`
	UpdatedAt            time.Time        `json:"updated_at"`
}

func (a AttemptIdentity) Validate() error {
	if strings.TrimSpace(a.AttemptID) == "" || strings.TrimSpace(a.WorkIntentID) == "" {
		return errors.New("attempt and work intent IDs are required")
	}
	if err := a.Change.Validate(); err != nil {
		return err
	}
	if err := a.Snapshot.Validate(); err != nil {
		return err
	}
	if a.Change.RepositoryID != a.Snapshot.RepositoryID {
		return errors.New("attempt change and snapshot repositories must match")
	}
	if strings.TrimSpace(a.PolicyDigest) == "" {
		return errors.New("attempt policy digest is required")
	}
	switch a.IntentKind {
	case IntentAutomatic, IntentManual, IntentHistorical, IntentLocal:
	default:
		return fmt.Errorf("unsupported work intent kind %q", a.IntentKind)
	}
	switch a.State {
	case AttemptAccepted, AttemptReady, AttemptInvestigating, AttemptWaiting,
		AttemptAssessed, AttemptIncomplete, AttemptFailed, AttemptCancelled, AttemptSuperseded:
	default:
		return fmt.Errorf("unsupported attempt state %q", a.State)
	}
	if a.Generation == 0 || a.Version == 0 {
		return errors.New("attempt generation and version must be positive")
	}
	if a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() || a.UpdatedAt.Before(a.CreatedAt) {
		return errors.New("attempt timestamps are invalid")
	}
	return nil
}

type ExecutionMode string

const (
	ExecutionService   ExecutionMode = "service"
	ExecutionEphemeral ExecutionMode = "ephemeral"
)

type PersistenceBoundary string

const (
	PersistenceDurableService PersistenceBoundary = "durable_service"
	PersistenceJobLocal       PersistenceBoundary = "job_local"
)

type ExecutionContext struct {
	Mode              ExecutionMode       `json:"mode"`
	ProducerID        string              `json:"producer_id"`
	PipelineID        string              `json:"pipeline_id,omitempty"`
	JobID             string              `json:"job_id,omitempty"`
	ComparisonTree    string              `json:"comparison_tree"`
	Persistence       PersistenceBoundary `json:"persistence"`
	TrustedBoundaryID string              `json:"trusted_boundary_id"`
	DeadlineAt        time.Time           `json:"deadline_at"`
}

func (c ExecutionContext) Validate() error {
	if c.Mode != ExecutionService && c.Mode != ExecutionEphemeral {
		return fmt.Errorf("unsupported execution mode %q", c.Mode)
	}
	if strings.TrimSpace(c.ProducerID) == "" || strings.TrimSpace(c.ComparisonTree) == "" || strings.TrimSpace(c.TrustedBoundaryID) == "" {
		return errors.New("execution producer, comparison tree and trusted boundary are required")
	}
	if c.DeadlineAt.IsZero() {
		return errors.New("execution deadline is required")
	}
	if c.Mode == ExecutionService && c.Persistence != PersistenceDurableService {
		return errors.New("service execution requires durable service persistence")
	}
	if c.Mode == ExecutionEphemeral && c.Persistence != PersistenceJobLocal {
		return errors.New("ephemeral execution requires job-local persistence")
	}
	return nil
}

type CheckStatus string

const (
	CheckPending       CheckStatus = "pending"
	CheckRunning       CheckStatus = "running"
	CheckSatisfied     CheckStatus = "satisfied"
	CheckViolated      CheckStatus = "violated"
	CheckUnknown       CheckStatus = "unknown"
	CheckNotApplicable CheckStatus = "not_applicable"
)

type Check struct {
	ID                  string      `json:"id"`
	MethodID            string      `json:"method_id"`
	Scope               string      `json:"scope"`
	Required            bool        `json:"required"`
	ApplicabilityReason string      `json:"applicability_reason"`
	Status              CheckStatus `json:"status"`
	EvidenceRefs        []string    `json:"evidence_refs,omitempty"`
	StatusReason        string      `json:"status_reason,omitempty"`
}

func (c Check) Validate() error {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.MethodID) == "" || strings.TrimSpace(c.Scope) == "" {
		return errors.New("check ID, method and scope are required")
	}
	if strings.TrimSpace(c.ApplicabilityReason) == "" {
		return errors.New("check applicability reason is required")
	}
	switch c.Status {
	case CheckSatisfied, CheckViolated:
		if len(c.EvidenceRefs) == 0 {
			return errors.New("satisfied or violated check requires evidence")
		}
	case CheckNotApplicable, CheckUnknown:
		if strings.TrimSpace(c.StatusReason) == "" {
			return errors.New("not-applicable or unknown check requires a reason")
		}
	case CheckPending, CheckRunning:
	default:
		return fmt.Errorf("unsupported check status %q", c.Status)
	}
	return nil
}

type Observation struct {
	ID              string    `json:"id"`
	ActionID        string    `json:"action_id"`
	Source          string    `json:"source"`
	RepositoryID    string    `json:"repository_id"`
	Revision        string    `json:"revision"`
	ObservedAt      time.Time `json:"observed_at"`
	ProducerVersion string    `json:"producer_version"`
	ContentDigest   string    `json:"content_digest"`
	Authority       string    `json:"authority"`
	Completeness    string    `json:"completeness"`
	SourceRefs      []string  `json:"source_refs,omitempty"`
}

func (o Observation) Validate() error {
	if strings.TrimSpace(o.ID) == "" || strings.TrimSpace(o.ActionID) == "" || strings.TrimSpace(o.Source) == "" {
		return errors.New("observation ID, action and source are required")
	}
	if strings.TrimSpace(o.RepositoryID) == "" || strings.TrimSpace(o.Revision) == "" {
		return errors.New("observation repository and revision are required")
	}
	if o.ObservedAt.IsZero() || strings.TrimSpace(o.ProducerVersion) == "" || strings.TrimSpace(o.ContentDigest) == "" {
		return errors.New("observation time, producer version and digest are required")
	}
	if strings.TrimSpace(o.Authority) == "" || strings.TrimSpace(o.Completeness) == "" {
		return errors.New("observation authority and completeness are required")
	}
	return nil
}

type AttemptEvent struct {
	ID              string            `json:"id"`
	AttemptID       string            `json:"attempt_id"`
	Type            string            `json:"type"`
	ActorPrincipal  string            `json:"actor_principal"`
	SubjectRevision string            `json:"subject_revision"`
	PayloadDigest   string            `json:"payload_digest"`
	ExpectedVersion uint64            `json:"expected_version"`
	OccurredAt      time.Time         `json:"occurred_at"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

func (e AttemptEvent) Validate() error {
	if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.AttemptID) == "" || strings.TrimSpace(e.Type) == "" {
		return errors.New("event ID, attempt and type are required")
	}
	if strings.TrimSpace(e.ActorPrincipal) == "" || strings.TrimSpace(e.SubjectRevision) == "" || strings.TrimSpace(e.PayloadDigest) == "" {
		return errors.New("event actor, revision and payload digest are required")
	}
	if e.ExpectedVersion == 0 || e.OccurredAt.IsZero() {
		return errors.New("event expected version and time are required")
	}
	return nil
}

type HumanDecision struct {
	ID              string    `json:"id"`
	ActorPrincipal  string    `json:"actor_principal"`
	ActorRole       string    `json:"actor_role"`
	Action          string    `json:"action"`
	SubjectID       string    `json:"subject_id"`
	SubjectVersion  uint64    `json:"subject_version"`
	SubjectRevision string    `json:"subject_revision"`
	Reason          string    `json:"reason"`
	DecidedAt       time.Time `json:"decided_at"`
}

func (d HumanDecision) Validate() error {
	if strings.TrimSpace(d.ID) == "" || strings.TrimSpace(d.ActorPrincipal) == "" || strings.TrimSpace(d.ActorRole) == "" {
		return errors.New("decision ID, actor and role are required")
	}
	if strings.TrimSpace(d.Action) == "" || strings.TrimSpace(d.SubjectID) == "" || d.SubjectVersion == 0 || strings.TrimSpace(d.SubjectRevision) == "" {
		return errors.New("decision action and versioned subject are required")
	}
	if strings.TrimSpace(d.Reason) == "" || d.DecidedAt.IsZero() {
		return errors.New("decision reason and time are required")
	}
	return nil
}

type ExecutionDecision struct {
	Sequence          uint64    `json:"sequence"`
	TriggerEventID    string    `json:"trigger_event_id"`
	InputArtifactRefs []string  `json:"input_artifact_refs"`
	Action            string    `json:"action"`
	Reason            string    `json:"reason"`
	CheckRefs         []string  `json:"check_refs,omitempty"`
	RiskDelta         string    `json:"risk_delta,omitempty"`
	CoverageDelta     string    `json:"coverage_delta,omitempty"`
	ReservationID     string    `json:"reservation_id,omitempty"`
	DecidedAt         time.Time `json:"decided_at"`
}

func (d ExecutionDecision) Validate() error {
	if d.Sequence == 0 || strings.TrimSpace(d.TriggerEventID) == "" {
		return errors.New("execution decision sequence and trigger event are required")
	}
	if strings.TrimSpace(d.Action) == "" || strings.TrimSpace(d.Reason) == "" || d.DecidedAt.IsZero() {
		return errors.New("execution decision action, reason and time are required")
	}
	if len(d.InputArtifactRefs) == 0 {
		return errors.New("execution decision requires immutable input references")
	}
	return nil
}

type InvestigationProjection struct {
	State          AttemptState        `json:"state"`
	Version        uint64              `json:"version"`
	Events         []AttemptEvent      `json:"events,omitempty"`
	Decisions      []ExecutionDecision `json:"decisions,omitempty"`
	HumanDecisions []HumanDecision     `json:"human_decisions,omitempty"`
	Observations   []Observation       `json:"observations,omitempty"`
	StopReason     string              `json:"stop_reason,omitempty"`
}

type CoverageProjection struct {
	Checks          []Check  `json:"checks,omitempty"`
	Complete        bool     `json:"complete"`
	UnknownCheckIDs []string `json:"unknown_check_ids,omitempty"`
}

type AssessmentCompleteness string

const (
	AssessmentPartial  AssessmentCompleteness = "partial"
	AssessmentComplete AssessmentCompleteness = "complete"
)

type AssessmentProjection struct {
	ID             string                 `json:"id"`
	Version        uint64                 `json:"version"`
	Purpose        string                 `json:"purpose"`
	SnapshotDigest string                 `json:"snapshot_digest"`
	PolicyDigest   string                 `json:"policy_digest"`
	Completeness   AssessmentCompleteness `json:"completeness"`
	Risk           string                 `json:"risk"`
	StopReason     string                 `json:"stop_reason"`
	FindingIDs     []string               `json:"finding_ids,omitempty"`
	EvidenceRefs   []string               `json:"evidence_refs,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
}

func (a AssessmentProjection) Validate(coverage CoverageProjection) error {
	if strings.TrimSpace(a.ID) == "" || a.Version == 0 || strings.TrimSpace(a.SnapshotDigest) == "" || strings.TrimSpace(a.PolicyDigest) == "" {
		return errors.New("assessment identity, snapshot and policy are required")
	}
	if a.Purpose != "assessment" && a.Purpose != "exploration" {
		return fmt.Errorf("unsupported assessment purpose %q", a.Purpose)
	}
	switch a.Completeness {
	case AssessmentComplete:
		if !coverage.Complete || len(coverage.UnknownCheckIDs) != 0 {
			return errors.New("complete assessment requires complete coverage")
		}
	case AssessmentPartial:
		if strings.TrimSpace(a.StopReason) == "" {
			return errors.New("partial assessment requires a stop reason")
		}
	default:
		return fmt.Errorf("unsupported assessment completeness %q", a.Completeness)
	}
	if strings.TrimSpace(a.Risk) == "" || a.CreatedAt.IsZero() {
		return errors.New("assessment risk and creation time are required")
	}
	return nil
}

type GateMode string

const (
	GateAdvisory GateMode = "advisory"
	GateBlocking GateMode = "blocking"
)

type GateOutcome string

const (
	GatePass       GateOutcome = "pass"
	GateViolations GateOutcome = "violations"
	GateIncomplete GateOutcome = "incomplete"
	GateError      GateOutcome = "error"
)

type GateResult struct {
	AssessmentID         string      `json:"assessment_id"`
	AssessmentVersion    uint64      `json:"assessment_version"`
	AssessmentDigest     string      `json:"assessment_digest"`
	PolicyDigest         string      `json:"policy_digest"`
	Mode                 GateMode    `json:"mode"`
	Outcome              GateOutcome `json:"outcome"`
	ViolatedRuleIDs      []string    `json:"violated_rule_ids,omitempty"`
	UnknownObligationIDs []string    `json:"unknown_obligation_ids,omitempty"`
	EvidenceRefs         []string    `json:"evidence_refs,omitempty"`
	EvaluatedAt          time.Time   `json:"evaluated_at"`
}

func (g GateResult) Clone() GateResult {
	g.ViolatedRuleIDs = append([]string(nil), g.ViolatedRuleIDs...)
	g.UnknownObligationIDs = append([]string(nil), g.UnknownObligationIDs...)
	g.EvidenceRefs = append([]string(nil), g.EvidenceRefs...)
	return g
}

func (g GateResult) Validate(coverage CoverageProjection) error {
	if strings.TrimSpace(g.AssessmentID) == "" || g.AssessmentVersion == 0 || strings.TrimSpace(g.AssessmentDigest) == "" || strings.TrimSpace(g.PolicyDigest) == "" {
		return errors.New("gate assessment and policy identity are required")
	}
	if g.Mode != GateAdvisory && g.Mode != GateBlocking {
		return fmt.Errorf("unsupported gate mode %q", g.Mode)
	}
	switch g.Outcome {
	case GatePass:
		if !coverage.Complete || len(coverage.UnknownCheckIDs) != 0 {
			return errors.New("gate cannot pass incomplete coverage")
		}
	case GateViolations:
		if len(g.ViolatedRuleIDs) == 0 {
			return errors.New("violations outcome requires violated rules")
		}
	case GateIncomplete:
		if coverage.Complete && len(g.UnknownObligationIDs) == 0 {
			return errors.New("incomplete outcome requires incomplete coverage or unknown obligations")
		}
	case GateError:
	default:
		return fmt.Errorf("unsupported gate outcome %q", g.Outcome)
	}
	if g.EvaluatedAt.IsZero() {
		return errors.New("gate evaluation time is required")
	}
	return nil
}

type DeliveryState string

const (
	DeliveryPendingAuthorization DeliveryState = "pending_authorization"
	DeliveryPending              DeliveryState = "pending"
	DeliverySending              DeliveryState = "sending"
	DeliveryDelivered            DeliveryState = "delivered"
	DeliveryUncertain            DeliveryState = "uncertain"
	DeliveryFailed               DeliveryState = "failed"
	DeliveryObsolete             DeliveryState = "obsolete"
)

type DeliveryRecord struct {
	OperationID     string        `json:"operation_id"`
	LogicalOutputID string        `json:"logical_output_id"`
	PayloadDigest   string        `json:"payload_digest"`
	State           DeliveryState `json:"state"`
	RemoteID        string        `json:"remote_id,omitempty"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

type DeliveryProjection struct {
	Records []DeliveryRecord `json:"records,omitempty"`
}

func (p DeliveryProjection) Clone() DeliveryProjection {
	p.Records = append([]DeliveryRecord(nil), p.Records...)
	return p
}
