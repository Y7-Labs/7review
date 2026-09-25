package review

import (
	"fmt"
	"sync"
	"time"
)

// Source is the single source of truth for one review run.
// The pipeline progressively enriches it from webhook input through SCM data,
// changed files, selected corpus, skills, memory, model findings, report, and
// run metadata.
type Source struct {
	Request   Request
	SCM       *SCMContext
	Attempt   AttemptIdentity
	Execution ExecutionContext

	ChangedFiles     []ChangedFile
	Diff             *StructuredDiff
	CorpusSections   []Section
	Evidence         []EvidenceItem
	SkillSections    []Section
	SkillActivations []SkillActivation
	Memory           MemoryRecall

	Model            ModelReview
	SkillCoverage    []SkillCoverage
	ToolRequests     []ToolRequest
	ToolObservations []ToolObservation
	Findings         []Finding
	HumanCheck       []Finding
	Notes            []Finding
	Questions        []Finding
	InlineComments   []InlineComment
	Report           Report
	Investigation    InvestigationProjection
	Coverage         CoverageProjection
	Assessment       AssessmentProjection
	Gate             GateResult
	Delivery         DeliveryProjection
	Run              RunMetadata
}

type ModelReview struct {
	RawResponses       []string `json:"raw_responses,omitempty"`
	ParseStatus        string   `json:"parse_status,omitempty"`
	ParseWarning       string   `json:"parse_warning,omitempty"`
	ParsedFindings     int      `json:"parsed_findings"`
	AcceptedFindings   int      `json:"accepted_findings"`
	HumanCheckFindings int      `json:"human_check_findings"`
	NoteFindings       int      `json:"note_findings"`
	QuestionFindings   int      `json:"question_findings"`
	RejectedFindings   int      `json:"rejected_findings"`
	ProviderTrace      string   `json:"provider_trace,omitempty"`
	RawResponseBytes   int      `json:"raw_response_bytes"`
	RawResponseExcerpt string   `json:"raw_response_excerpt,omitempty"`
}

type MemoryRecall struct {
	Conventions []string
	Decisions   []string
	History     []string
}

type Report struct {
	Draft string
	Final string
}

type RunMetadata struct {
	ID             string
	StartedAt      time.Time
	StepProviders  map[string]string
	AvailableTools []string
	Warnings       []string
}

// Context wraps the canonical Source with transient execution helpers retained
// for compatibility while the pipeline migrates to Source-only stage inputs.
type Context struct {
	mu sync.Mutex

	Source

	// ── Review findings (populated by Step 5, thread-safe) ───────────────
	// Each parallel batch appends its raw findings string here.
	rawFindings []string

	// ── HIL decision (populated by HIL gate) ─────────────────────────────
	HILApproved bool
	// HILEdits are finding IDs the human marked as false positives or added.
	HILRejectedIDs []string
	HILAddedNotes  []string
}

// NewContext initialises the canonical source and transient helpers for one run.
func NewContext(req Request) *Context {
	stepProviders := make(map[string]string)
	rc := &Context{
		Source: Source{
			Request: req,
			Run: RunMetadata{
				StartedAt:     time.Now().UTC(),
				StepProviders: stepProviders,
			},
		},
	}
	return rc
}

// NewCanonicalContext validates target identity and execution boundaries before
// creating a context. Legacy callers continue to use NewContext until migration.
func NewCanonicalContext(req Request, attempt AttemptIdentity, execution ExecutionContext) (*Context, error) {
	if err := attempt.Validate(); err != nil {
		return nil, err
	}
	if err := execution.Validate(); err != nil {
		return nil, err
	}
	if req.ProjectID != "" && req.ProjectID != attempt.Change.RepositoryID {
		return nil, fmt.Errorf("request project %q does not match attempt repository %q", req.ProjectID, attempt.Change.RepositoryID)
	}
	rc := NewContext(req)
	rc.Source.Attempt = attempt
	rc.Source.Execution = execution
	rc.Source.Investigation = InvestigationProjection{State: attempt.State, Version: attempt.Version}
	return rc, nil
}

// AddFindings appends raw findings from one parallel batch.
// Thread-safe — called concurrently by Step 5 goroutines.
func (rc *Context) AddFindings(findings string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.rawFindings = append(rc.rawFindings, findings)
}

// AllFindings returns the merged findings from all batches.
func (rc *Context) AllFindings() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := make([]string, len(rc.rawFindings))
	copy(out, rc.rawFindings)
	return out
}

// Clone returns a detached execution context without copying its mutex.
func (rc *Context) Clone() *Context {
	if rc == nil {
		return nil
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return &Context{
		Source:         rc.Source.Clone(),
		rawFindings:    append([]string(nil), rc.rawFindings...),
		HILApproved:    rc.HILApproved,
		HILRejectedIDs: append([]string(nil), rc.HILRejectedIDs...),
		HILAddedNotes:  append([]string(nil), rc.HILAddedNotes...),
	}
}

// RecordProvider logs which provider handled a given step.
func (rc *Context) RecordProvider(step, providerAndModel string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.Run.StepProviders[step] = providerAndModel
}

func (rc *Context) AddWarning(warning string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.Run.Warnings = append(rc.Run.Warnings, warning)
}

// ChangedPaths returns the list of file paths in the structured diff.
// Convenience method used by Steps 3 and 4.
func (rc *Context) ChangedPaths() []string {
	diff := rc.Source.Diff
	if diff == nil {
		return nil
	}
	paths := make([]string, len(diff.Files))
	for i, f := range diff.Files {
		paths[i] = f.Path
	}
	return paths
}
