package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

type PreparedService struct {
	Admission    review.Admission
	Source       review.Source
	Continuation []byte
	Ignored      bool
	Reason       string
}
type acceptedContinuation struct {
	Version int
	Rules   []policy.ResolvedRule
}
type acceptedExecution struct {
	aggregate review.AcceptedAggregate
	rules     []policy.ResolvedRule
}

// PrepareService performs trusted SCM/policy reads only. No job, attempt, local
// run record, model call or publication is created before aggregate acceptance.
func (p *Pipeline) PrepareService(ctx context.Context, req review.Request, deadline time.Time) (PreparedService, error) {
	if p == nil || p.Canonical == nil || p.Canonical.Authority == nil || p.SCM == nil || p.TrustedPolicy == nil || p.DeliveryMode != DeliveryArtifactOnly {
		return PreparedService{}, fmt.Errorf("service preparation requires canonical artifact-only admission")
	}
	c := p.Canonical
	if c.Execution.Mode != review.ExecutionService || c.Execution.Persistence != review.PersistenceDurableService {
		return PreparedService{}, fmt.Errorf("service preparation has invalid execution mode")
	}
	if !c.Execution.DeadlineAt.IsZero() && c.Execution.DeadlineAt.Before(deadline) {
		deadline = c.Execution.DeadlineAt
	}
	if deadline.IsZero() || !time.Now().Before(deadline) {
		return PreparedService{}, fmt.Errorf("service preparation deadline expired")
	}
	change, err := c.change(req)
	if err != nil {
		return PreparedService{}, err
	}
	generation, err := c.Authority.ObserveGeneration(ctx, change)
	if err != nil {
		return PreparedService{}, err
	}
	scm, err := p.SCM.Enrich(ctx, req)
	if err != nil {
		return PreparedService{}, err
	}
	if scm == nil {
		return PreparedService{}, fmt.Errorf("service preparation has no SCM evidence")
	}
	rc := review.NewContext(req)
	applySCMContext(rc, scm)
	rc.Source.Run.AvailableTools = []string{"scm", "diff", "context", "review", "report", "memory"}
	rc.Diff = normalizeDiff(scm.Files)
	rc.Request.ChangedPaths = rc.ChangedPaths()
	trusted, err := p.TrustedPolicy.Evaluate(ctx, rc.Request, scm)
	if err != nil {
		return PreparedService{}, err
	}
	if !trusted.Enforced || trusted.Mode != "enforce" || !trusted.Attestation.Verified {
		return PreparedService{}, fmt.Errorf("service preparation requires verified enforced policy")
	}
	rc.Source.Policy = projectTrustedPolicy(trusted)
	att := trusted.Attestation
	rc.Source.Attestation = &att
	if trusted.Warning != "" {
		rc.AddWarning(trusted.Warning)
	}
	if !trusted.Trigger.Accepted {
		return PreparedService{Ignored: true, Reason: strings.Join(trusted.Trigger.Reasons, "; ")}, nil
	}
	var rules []policy.ResolvedRule
	if trusted.Effective != nil {
		rules = trusted.Effective.Rules
	}
	p.selectPolicySkills(rc, trusted.Methods, rules)
	execution := c.Execution
	execution.ComparisonTree = att.ComparisonTree
	execution.DeadlineAt = deadline.UTC()
	kind := review.IntentAutomatic
	if req.EventAction == "manual" || req.DeliveryID == "" {
		kind = review.IntentManual
	}
	input := review.Admission{Request: rc.Request, Change: change, Attestation: att, Policy: rc.Source.Policy, Execution: execution, IntentKind: kind, ExpectedGeneration: generation, DeliveryID: req.DeliveryID, PayloadDigest: req.PayloadDigest}
	continuation, err := json.Marshal(acceptedContinuation{Version: 1, Rules: rules})
	if err != nil {
		return PreparedService{}, err
	}
	return PreparedService{Admission: input, Source: rc.Source.Clone(), Continuation: continuation}, nil
}

// RunAccepted starts only from the committed checkpoint. It never re-attests or
// re-admits the request using newer remote state.
func (p *Pipeline) RunAccepted(ctx context.Context, aggregate review.AcceptedAggregate) error {
	if p == nil || p.Canonical == nil || p.DeliveryMode != DeliveryArtifactOnly {
		return fmt.Errorf("accepted execution requires canonical pipeline")
	}
	s := aggregate.Source
	c := p.Canonical
	change, err := c.change(aggregate.OriginalRequest)
	if err != nil {
		return err
	}
	if aggregate.Version != 1 || s.Run.ID == "" || s.Attempt.Change != change || !reflect.DeepEqual(s.Attempt, aggregate.Receipt.Attempt) || !reflect.DeepEqual(s.Policy, aggregate.Receipt.Policy) || !reflect.DeepEqual(s.Execution, aggregate.Receipt.Execution) || s.Execution.Mode != c.Execution.Mode || s.Execution.Persistence != c.Execution.Persistence || s.Execution.ProducerID != c.Execution.ProducerID || s.Execution.TrustedBoundaryID != c.Execution.TrustedBoundaryID || !s.Policy.Enforced || !s.Policy.TriggerAccepted {
		return fmt.Errorf("accepted execution changed trusted authority")
	}
	if !reflect.DeepEqual(s.Attestation, &aggregate.Receipt.Attestation) || s.Execution.PipelineID != c.Execution.PipelineID || s.Execution.JobID != c.Execution.JobID {
		return fmt.Errorf("accepted execution changed identity proof")
	}
	if _, err = review.NewVerifiedContext(s.Request, s.Attempt, s.Execution, aggregate.Receipt.Attestation); err != nil {
		return err
	}
	var continuation acceptedContinuation
	if err = json.Unmarshal(aggregate.Continuation, &continuation); err != nil {
		return err
	}
	if continuation.Version != 1 {
		return fmt.Errorf("unsupported accepted continuation version")
	}
	return p.run(ctx, aggregate.OriginalRequest, s.Run.ID, &acceptedExecution{aggregate: aggregate, rules: continuation.Rules})
}

func (p *Pipeline) selectPolicySkills(rc *review.Context, methods []ResolvedPolicyMethod, rules []policy.ResolvedRule) {
	if p.SkillLoader != nil && !rc.Source.Policy.Enforced {
		rc.Source.SkillActivations = p.SkillLoader.SelectActivations(rc.Request)
		rc.Source.SkillSections = p.SkillLoader.Select(rc.Request)
	}
	for _, method := range methods {
		rc.Source.SkillActivations = append(rc.Source.SkillActivations, method.Activation)
		rc.Source.SkillSections = append(rc.Source.SkillSections, method.Section)
	}
	for _, rule := range rules {
		rc.Source.SkillActivations = append(rc.Source.SkillActivations, review.SkillActivation{Name: "rule:" + rule.ID, Path: rule.Source.Path, Category: "policy", Required: true, RequiredChecks: append([]string(nil), rule.Checks...), Reason: "required by selected trusted rule"})
	}
	rc.Source.SkillActivations = mergeEffectivePolicyActivation(rc.Source.SkillActivations, rc.Source.Policy)
}
