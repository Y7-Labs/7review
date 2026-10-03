package ci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/skills"
	"github.com/Y4NN777/7review/agent/tools"
)

type ArtifactRef struct {
	Provider string
	ID       int64
	Path     string
}

type Options struct {
	RepositoryDir   string
	PolicyPath      string
	SkillsPath      string
	Offline         bool
	Environment     Environment
	DeadlineAt      time.Time
	Quality         []ArtifactRef
	DependencyGraph map[string][]string
	Orchestrator    *orchestrator.Orchestrator
	HTTPClient      *http.Client
	Now             func() time.Time
}

type Result struct {
	Snapshot          review.SnapshotIdentity
	Attestation       review.SnapshotAttestation
	Execution         review.ExecutionContext
	EffectivePolicy   policy.EffectivePolicy
	Source            review.Source
	Assessment        review.AssessmentProjection
	Coverage          review.CoverageProjection
	Gate              review.GateResult
	ImportedEvidence  []review.ImportedCIQualityEvidence
	IncompleteReasons []string
	Report            string
	StartedAt         time.Time
	CompletedAt       time.Time
}

func Run(ctx context.Context, opts Options) (Result, error) {
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now().UTC()
	}
	result := Result{StartedAt: now}
	if strings.TrimSpace(opts.RepositoryDir) == "" || strings.TrimSpace(opts.PolicyPath) == "" || opts.DeadlineAt.IsZero() {
		return result, errors.New("CI runner requires repository, policy path and deadline")
	}
	if opts.DependencyGraph != nil {
		if err := ValidateDependencyGraph(opts.Environment.JobID, opts.DependencyGraph); err != nil {
			return result, err
		}
	}
	adapter := tools.FrozenGitSCM{
		RepositoryDir: opts.RepositoryDir, RepositoryID: opts.Environment.RepositoryID, Provider: opts.Environment.Provider,
		BaseRevision: opts.Environment.BaseRevision, HeadRevision: opts.Environment.HeadRevision,
		ComparisonRevision: opts.Environment.MergeRevision, Offline: opts.Offline,
	}
	snapshot, err := adapter.Snapshot(ctx)
	if err != nil {
		return result, err
	}
	result.Snapshot = snapshot
	execution := review.ExecutionContext{
		Mode: review.ExecutionEphemeral, ProducerID: opts.Environment.Provider + ":7review-ci",
		PipelineID: opts.Environment.PipelineID, JobID: opts.Environment.JobID,
		ComparisonTree: snapshot.AnalysisRevision(), Persistence: review.PersistenceJobLocal,
		TrustedBoundaryID: opts.Environment.Provider + ":" + opts.Environment.RepositoryID,
		DeadlineAt:        opts.DeadlineAt.UTC(),
	}
	if err := execution.Validate(); err != nil {
		return result, err
	}
	result.Execution = execution
	req := review.Request{
		Provider: opts.Environment.Provider, EventAction: "manual", ProjectID: opts.Environment.RepositoryID,
		Repository: opts.Environment.RepositoryID, ChangeID: opts.Environment.ChangeID,
		Title: opts.Environment.Title, Description: opts.Environment.Description, WebURL: opts.Environment.WebURL,
		SourceSHA: snapshot.HeadRevision, TargetSHA: snapshot.BaseRevision,
		SourceBranch: opts.Environment.SourceBranch, TargetBranch: opts.Environment.TargetBranch, Author: opts.Environment.Author,
	}
	scm, err := adapter.Enrich(ctx, req)
	if err != nil {
		return result, err
	}
	admission := pipeline.FrozenPolicyAdmission{
		Path: opts.PolicyPath, Reader: adapter, RuntimeCapabilities: []string{"repo.read", "model.review"}, Now: opts.Now,
	}
	policyResult, err := admission.Evaluate(ctx, req, scm)
	if err != nil {
		return result, err
	}
	result.Attestation = policyResult.Attestation
	result.EffectivePolicy = *policyResult.Effective

	evidence, evidenceErrors := importQualityEvidence(ctx, opts, snapshot, execution)
	result.ImportedEvidence = evidence
	result.IncompleteReasons = append(result.IncompleteReasons, evidenceErrors...)

	source := minimalSource(req, scm, policyResult, execution)
	if opts.Environment.UntrustedFork {
		result.IncompleteReasons = append(result.IncompleteReasons, "model execution is disabled for an untrusted fork")
	} else if opts.Orchestrator == nil {
		result.IncompleteReasons = append(result.IncompleteReasons, "model provider credentials are unavailable")
	} else {
		temporaryRoot, err := os.MkdirTemp("", "7review-ci-snapshot-*")
		if err != nil {
			return result, err
		}
		defer os.RemoveAll(temporaryRoot)
		if err := adapter.ExportSnapshot(ctx, temporaryRoot); err != nil {
			return result, err
		}
		skillPath := opts.SkillsPath
		if skillPath == "" {
			skillPath = ".7review/skills"
		}
		if path.IsAbs(skillPath) || path.Clean(skillPath) == ".." || strings.HasPrefix(path.Clean(skillPath), "../") {
			return result, errors.New("CI skills path must stay inside the frozen snapshot")
		}
		loader := &skills.Loader{SkillsDir: filepath.Join(temporaryRoot, filepath.FromSlash(skillPath))}
		if err := loader.Load(); err != nil {
			return result, err
		}
		store := pipeline.NewMemoryRunStore()
		reviewPipeline := &pipeline.Pipeline{
			Config:      &config.Config{CorpusRoot: temporaryRoot, MaxDiffTokens: 6000, MaxSupportingCorpusSections: 3},
			SkillLoader: loader, Orchestrator: opts.Orchestrator, Jobs: store,
			Policy: pipeline.DefaultPolicyFilter{}, FindingValidator: pipeline.DefaultFindingValidator{},
			Memory: pipeline.NoopMemoryStore{}, SCM: adapter, ContextReducer: pipeline.NoopContextReducer{},
			TrustedPolicy: fixedPolicyAdmission{result: policyResult}, DeliveryMode: pipeline.DeliveryArtifactOnly,
		}
		if runErr := reviewPipeline.Run(ctx, req); runErr != nil {
			result.IncompleteReasons = append(result.IncompleteReasons, "review execution failed: "+runErr.Error())
		} else {
			runs, listErr := store.List(ctx)
			if listErr != nil || len(runs) != 1 || runs[0].Source == nil {
				return result, errors.New("CI runner could not recover the artifact-only review result")
			}
			source = runs[0].Source.Clone()
			source.Execution = execution
		}
	}

	assessment := buildAssessment(result, source, now)
	coverage, gate, err := evaluateResult(result.EffectivePolicy, assessment, source, evidence, now)
	if err != nil {
		return result, err
	}
	if len(result.IncompleteReasons) > 0 || gate.Outcome == review.GateIncomplete {
		assessment.Completeness = review.AssessmentPartial
		assessment.StopReason = strings.Join(uniqueStrings(append(result.IncompleteReasons, coverage.UnknownCheckIDs...)), "; ")
		coverage, gate, err = evaluateResult(result.EffectivePolicy, assessment, source, evidence, now)
		if err != nil {
			return result, err
		}
	}
	source.Assessment, source.Coverage, source.Gate = assessment, coverage, gate
	result.Source, result.Assessment, result.Coverage, result.Gate = source, assessment, coverage, gate
	result.Report = source.Report.Draft
	if strings.TrimSpace(result.Report) == "" {
		result.Report = partialReport(result)
	}
	result.CompletedAt = currentTime(opts.Now)
	return result, nil
}

func importQualityEvidence(ctx context.Context, opts Options, snapshot review.SnapshotIdentity, execution review.ExecutionContext) ([]review.ImportedCIQualityEvidence, []string) {
	var imported []review.ImportedCIQualityEvidence
	var failures []string
	artifactExecution := execution
	artifactExecution.JobID = ""
	for _, ref := range opts.Quality {
		var evidence review.ImportedCIQualityEvidence
		var err error
		switch ref.Provider {
		case "github":
			verifier := tools.GitHubCIArtifactVerifier{BaseURL: os.Getenv("GITHUB_API_URL"), Token: os.Getenv("GITHUB_TOKEN"), Repository: opts.Environment.RepositoryID, Client: opts.HTTPClient, Now: opts.Now}
			evidence, err = verifier.VerifyAndImport(ctx, ref.ID, ref.Path, snapshot, artifactExecution)
		case "gitlab":
			verifier := tools.GitLabCIArtifactVerifier{BaseURL: firstEnvironment("CI_SERVER_URL", "GITLAB_URL"), Token: os.Getenv("GITLAB_TOKEN"), JobToken: os.Getenv("CI_JOB_TOKEN"), ProjectID: opts.Environment.RepositoryID, Client: opts.HTTPClient, Now: opts.Now}
			evidence, err = verifier.VerifyAndImport(ctx, ref.ID, ref.Path, snapshot, artifactExecution)
		default:
			err = fmt.Errorf("unsupported quality artifact provider %q", ref.Provider)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("quality artifact %s:%d:%s: %v", ref.Provider, ref.ID, ref.Path, err))
			continue
		}
		imported = append(imported, evidence)
	}
	return imported, failures
}

func buildAssessment(result Result, source review.Source, now time.Time) review.AssessmentProjection {
	digest, _ := result.Attestation.Digest()
	ids := make([]string, 0, len(source.Findings))
	for _, finding := range source.Findings {
		ids = append(ids, finding.ID)
	}
	sort.Strings(ids)
	assessment := review.AssessmentProjection{
		ID: "assessment-" + shortDigest(digest), Version: 1, Purpose: "assessment", SnapshotDigest: digest,
		PolicyDigest: result.EffectivePolicy.Digest, Completeness: review.AssessmentComplete,
		Risk: assessmentRisk(source.Findings, result.EffectivePolicy.RiskFloor), FindingIDs: ids, CreatedAt: now,
	}
	if len(result.IncompleteReasons) > 0 {
		assessment.Completeness = review.AssessmentPartial
		assessment.StopReason = strings.Join(uniqueStrings(result.IncompleteReasons), "; ")
	}
	return assessment
}

func evaluateResult(effective policy.EffectivePolicy, assessment review.AssessmentProjection, source review.Source, evidence []review.ImportedCIQualityEvidence, now time.Time) (review.CoverageProjection, review.GateResult, error) {
	data, err := json.Marshal(assessment)
	if err != nil {
		return review.CoverageProjection{}, review.GateResult{}, err
	}
	sum := sha256.Sum256(data)
	return pipeline.EvaluateReviewAndImportedCIGate(effective, assessment, "sha256:"+hex.EncodeToString(sum[:]), source, evidence, nil, now)
}

func minimalSource(req review.Request, scm *review.SCMContext, admitted pipeline.TrustedPolicyResult, execution review.ExecutionContext) review.Source {
	source := review.Source{Request: req, SCM: scm, Execution: execution, ChangedFiles: append([]review.ChangedFile(nil), scm.Files...)}
	attestation := admitted.Attestation
	source.Attestation = &attestation
	if admitted.Effective != nil {
		source.Policy = review.PolicyProjection{Mode: admitted.Mode, Digest: admitted.Effective.Digest, SourceRevision: admitted.Source.Revision, SourceDigest: admitted.Source.Digest, Methods: append([]string(nil), admitted.Effective.Methods...), RequiredChecks: append([]string(nil), admitted.Effective.RequiredChecks...), MatchedPacks: append([]string(nil), admitted.Effective.MatchedPacks...), RiskFloor: admitted.Effective.RiskFloor, IndependentReview: admitted.Effective.IndependentReview, TriggerAccepted: true}
	}
	return source
}

func assessmentRisk(findings []review.Finding, floor string) string {
	rank := map[string]int{"none": 0, "info": 1, "low": 2, "medium": 3, "high": 4, "critical": 5}
	risk := floor
	for _, finding := range findings {
		if rank[string(finding.Severity)] > rank[risk] {
			risk = string(finding.Severity)
		}
	}
	if risk == "" {
		return "none"
	}
	return risk
}

func partialReport(result Result) string {
	return "# 7review CI assessment\n\nStatus: incomplete\n\n" + strings.Join(result.IncompleteReasons, "\n") + "\n"
}

func shortDigest(value string) string {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) > 16 {
		return value[:16]
	}
	return value
}

func uniqueStrings(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
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

func currentTime(now func() time.Time) time.Time {
	if now != nil {
		return now().UTC()
	}
	return time.Now().UTC()
}

type fixedPolicyAdmission struct{ result pipeline.TrustedPolicyResult }

func (a fixedPolicyAdmission) Evaluate(context.Context, review.Request, *review.SCMContext) (pipeline.TrustedPolicyResult, error) {
	return a.result, nil
}

func ExitCode(result Result) int { return policy.GateExitCode(result.Gate) }

func ParseArtifactRef(value string) (ArtifactRef, error) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || (parts[0] != "github" && parts[0] != "gitlab") || strings.TrimSpace(parts[2]) == "" {
		return ArtifactRef{}, errors.New("quality artifact must use provider:id:path")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return ArtifactRef{}, errors.New("quality artifact ID must be positive")
	}
	return ArtifactRef{Provider: parts[0], ID: id, Path: parts[2]}, nil
}
