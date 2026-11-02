package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

type ModelContinuationV1 struct{}

// BuildModelContinuation reconstructs only a typed formatter request. It does
// not grant a job token, action identity, reservation or permission to send.
func (ModelContinuationV1) BuildModelContinuation(parent review.ModelReduction) (orchestrator.ModelContinuationPlan, error) {
	var empty orchestrator.ModelContinuationPlan
	if parent.Version != 1 || parent.NextCursor == nil || parent.Routing == nil || parent.Source.Report.Final != "" || parent.Source.Policy.OperatorCeilings == nil {
		return empty, errors.New("qualified parent continuation unavailable")
	}
	if err := parent.Routing.Validate(); err != nil {
		return empty, err
	}
	chain, ok := parent.Routing.Roles["formatter"]
	if !ok || len(chain.Routes) == 0 {
		return empty, errors.New("original formatter role unavailable")
	}
	rc := &review.Context{Source: parent.Source.Clone()}
	for _, raw := range parent.RawResponses {
		rc.AddFindings(raw)
	}
	step, system, user := "", "", ""
	switch parent.State {
	case review.ModelReductionNeedsFindingsRepair:
		if parent.NextCursor.Phase != review.ModelRepairFindings || strings.TrimSpace(parent.RepairInput) == "" {
			return empty, errors.New("invalid findings repair continuation")
		}
		step, system, user = "repair_findings", repairFindingsSystemPrompt(), repairFindingsUserMessage(parent.RepairInput)
	case review.ModelReductionNeedsCoverageRepair:
		if parent.NextCursor.Phase != review.ModelRepairCoverage {
			return empty, errors.New("invalid coverage repair continuation")
		}
		step, system, user = "repair_skill_coverage", repairSkillCoverageSystemPrompt(), repairSkillCoverageUserMessage(rc)
	default:
		return empty, errors.New("continuation requires committed tool observations or is terminal")
	}
	ctx, err := review.WithModelCheckpoint(review.WithModelRouting(review.WithModelValidation(context.Background(), &parent.Validation), parent.Routing), rc, *parent.NextCursor)
	if err != nil {
		return empty, err
	}
	route := chain.Routes[0]
	payload, err := json.Marshal(orchestrator.LLMRequest{Model: route.Model, MaxTokens: chain.MaxTokens, SystemPrompt: system, UserMessage: user})
	if err != nil {
		return empty, err
	}
	s := rc.Source.Clone()
	i := orchestrator.DispatchIntent{Invocation: "complete", Provider: route.Provider, Model: route.Model, Role: "formatter", Step: step, Payload: payload, RequestDigest: modelDigest(payload), ModelCheckpoint: review.ModelCheckpointFromContext(ctx), Attempt: s.Attempt, Execution: s.Execution, Attestation: s.Attestation, PolicyDigest: s.Policy.Digest, PolicyLimits: s.Policy.Limits, OperatorCeilings: s.Policy.OperatorCeilings.Clone()}
	if err = orchestrator.ValidateModelCheckpoint(i); err != nil {
		return empty, err
	}
	return orchestrator.ModelContinuationPlan{Version: 1, ParentGroupDigest: parent.GroupDigest, ParentInputDigest: parent.InputDigest, Intent: i}, nil
}
