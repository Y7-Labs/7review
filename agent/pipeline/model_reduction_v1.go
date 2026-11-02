package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

type ModelReducerV1 struct{}

func modelDigest(raw []byte) string {
	s := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(s[:])
}

// ReduceModelResults consumes only immutable recorded data. It never calls a
// provider, tool, store, publisher or current runtime/profile configuration.
func (ModelReducerV1) ReduceModelResults(ctx context.Context, results []orchestrator.RecordedModelResult) (review.ModelReduction, error) {
	var empty review.ModelReduction
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if len(results) == 0 || len(results) > 128 {
		return empty, errors.New("invalid model reduction group bound")
	}
	type slot struct {
		result orchestrator.RecordedModelResult
		cp     review.ModelCheckpoint
	}
	slots := make([]slot, 0, len(results))
	group := ""
	size := 0
	seen := map[string]bool{}
	for _, r := range results {
		i, o := r.Intent, r.Outcome
		size += len(i.ModelCheckpoint) + len(i.Payload) + len(o.Response)
		if size > 64<<20 || len(o.Response) > 8<<20 || !o.ResponseReceived || o.ElapsedMS < 0 || modelDigest(o.Response) != o.ResponseDigest || modelDigest(i.Payload) != i.RequestDigest || i.ActionID == "" || seen[i.ActionID] {
			return empty, errors.New("invalid or duplicate recorded model result")
		}
		seen[i.ActionID] = true
		if i.Invocation != "complete" && i.Invocation != "stream" {
			return empty, errors.New("unqualified recorded invocation")
		}
		if err := orchestrator.ValidateModelCheckpoint(i); err != nil {
			return empty, err
		}
		cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
		if err != nil {
			return empty, err
		}
		if cp.Validation == nil || cp.Validation.Version != 1 || cp.Source.Report.Final != "" {
			return empty, errors.New("recorded validation contract unavailable or final authority contaminated")
		}
		if cp.Source.Diff == nil || len(cp.Source.Diff.Files) == 0 {
			return empty, errors.New("model reduction requires frozen changed-file evidence")
		}
		var req orchestrator.LLMRequest
		if err = json.Unmarshal(i.Payload, &req); err != nil {
			return empty, err
		}
		canonical, err := json.Marshal(req)
		if err != nil || !bytes.Equal(canonical, i.Payload) || req.Model != i.Model || req.MaxTokens <= 0 {
			return empty, errors.New("recorded request changed model or schema")
		}
		key, err := review.ModelCheckpointGroupDigest(i.ModelCheckpoint)
		if err != nil {
			return empty, err
		}
		if group != "" && group != key {
			return empty, errors.New("recorded results belong to different continuation groups")
		}
		group = key
		slots = append(slots, slot{r, cp})
	}
	sort.Slice(slots, func(a, b int) bool { return slots[a].cp.Cursor.BatchIndex < slots[b].cp.Cursor.BatchIndex })
	cp := slots[0].cp
	want := 1
	if cp.Cursor.Phase == review.ModelReviewBatches {
		want = cp.Cursor.BatchCount
	}
	if want != len(slots) {
		return empty, errors.New("model reduction requires every batch slot")
	}
	for index, s := range slots {
		if s.cp.Cursor.BatchIndex != index {
			return empty, errors.New("duplicate or missing model batch slot")
		}
	}
	rc := &review.Context{Source: cp.Source.Clone()}
	if rc.Source.Run.StepProviders == nil {
		rc.Source.Run.StepProviders = map[string]string{}
	}
	rc.Source.Report = review.Report{}
	for _, raw := range cp.RawResponses {
		rc.AddFindings(raw)
	}
	reduced := review.ModelReduction{Version: 1, GroupDigest: group, Cursor: cp.Cursor, Validation: *cp.Validation}
	var responses []string
	for _, s := range slots {
		i, o := s.result.Intent, s.result.Outcome
		reduced.Inputs = append(reduced.Inputs, review.ModelResultRef{ActionID: i.ActionID, RequestDigest: i.RequestDigest, ResponseDigest: o.ResponseDigest, CheckpointDigest: modelDigest(i.ModelCheckpoint)})
		responses = append(responses, string(o.Response))
		rc.AddFindings(string(o.Response))
		rc.RecordProvider(i.Step, i.Provider+"/"+i.Model)
	}
	refs, _ := json.Marshal(reduced.Inputs)
	reduced.InputDigest = modelDigest(refs)
	var findings []review.Finding
	var coverage []review.SkillCoverage
	var requests []review.ToolRequest
	status, warning := "", ""
	rawForRepair := strings.Join(responses, "\n")
	switch cp.Cursor.Phase {
	case review.ModelRepairCoverage:
		findings = cp.Source.Findings
		coverage = cp.Cursor.PriorCoverage
		if c, ok := parseSkillCoverage(responses[0]); ok && len(c) > 0 {
			coverage = c
		}
		status, warning = cp.Source.Model.ParseStatus, cp.Source.Model.ParseWarning
	case review.ModelRepairFindings:
		findings, _, requests, status = parseReviewOutputDetailed(responses[0])
		coverage = cp.Cursor.PriorCoverage
		if len(requests) > 0 {
			return empty, errors.New("formatter findings repair cannot request tools")
		}
		warning = "model returned malformed findings JSON; formatter repaired it before validation"
	default:
		findings, coverage, requests, status = parseReviewResponses(responses)
		if cp.Cursor.Phase == review.ModelToolRound && len(coverage) == 0 {
			coverage = cp.Cursor.PriorCoverage
		}
	}
	finish := func() (review.ModelReduction, error) {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		reduced.Source = rc.Source.Clone()
		reduced.RawResponses = rc.AllFindings()
		raw, err := json.Marshal(reduced)
		if err != nil {
			return empty, err
		}
		if len(raw) > 32<<20 {
			return empty, errors.New("model reduction artifact exceeds storage bound")
		}
		return reduced, nil
	}
	if len(requests) > 0 {
		round := 1
		if cp.Cursor.Phase == review.ModelToolRound {
			round = cp.Cursor.Round + 1
		}
		if round > review.MaxModelToolRounds {
			reduced.State = review.ModelReductionBlocked
			reduced.Reason = "model requested tools beyond the qualified round bound"
			return finish()
		}
		reduced.State = review.ModelReductionNeedsTools
		reduced.ToolRequests = markToolRequestRound(requests, round)
		reduced.NextCursor = &review.ModelCursor{Phase: review.ModelToolRound, Round: round, PriorCoverage: coverage}
		return finish()
	}
	if status == "unparseable" && cp.Cursor.Phase != review.ModelRepairFindings {
		reduced.State = review.ModelReductionNeedsFindingsRepair
		reduced.RepairInput = rawForRepair
		reduced.NextCursor = &review.ModelCursor{Phase: review.ModelRepairFindings, PriorCoverage: coverage}
		return finish()
	}
	if status != "parsed" && status != "empty_findings" {
		reduced.State = review.ModelReductionBlocked
		reduced.Reason = "model response is not a complete structured review"
		rc.Source.Model = modelReviewAudit(rc, findings, nil, status, warning)
		return finish()
	}
	if status == "empty_findings" && warning == "" {
		warning = "model returned an explicit empty findings list; verify manually when selected evidence contains contract or rule obligations"
	}
	rc.Source.Findings = findings
	rc.Source.SkillCoverage = coverage
	rc.Source.Model = modelReviewAudit(rc, findings, nil, status, warning)
	if warning != "" {
		rc.AddWarning(warning)
	}
	if len(coreSkillCoverageErrors(rc.Source.SkillActivations, coverage)) > 0 && cp.Cursor.Phase != review.ModelRepairCoverage {
		reduced.State = review.ModelReductionNeedsCoverageRepair
		reduced.NextCursor = &review.ModelCursor{Phase: review.ModelRepairCoverage, PriorCoverage: coverage}
		return finish()
	}
	if len(coreSkillCoverageErrors(rc.Source.SkillActivations, rc.Source.SkillCoverage)) > 0 {
		rc.Source.SkillCoverage = mergeSkillCoverage(rc.Source.SkillCoverage, synthesizeCoreSkillCoverage(rc.Source.SkillActivations, rc))
	}
	if len(validateSkillCoverage(rc.Source.SkillActivations, rc.Source.SkillCoverage)) > 0 {
		rc.Source.SkillCoverage = mergeSkillCoverage(rc.Source.SkillCoverage, synthesizeProviderAPISkillCoverage(rc.Source.SkillActivations, rc))
	}
	for _, w := range validateSkillCoverage(rc.Source.SkillActivations, rc.Source.SkillCoverage) {
		rc.AddWarning(w)
	}
	if len(coreSkillCoverageErrors(rc.Source.SkillActivations, rc.Source.SkillCoverage)) > 0 {
		reduced.State = review.ModelReductionBlocked
		reduced.Reason = "required core skill coverage remains unsatisfied"
		return finish()
	}
	findings, _ = normalizeFindingLocations(rc, findings)
	validation, err := (DefaultFindingValidator{MinConfidence: cp.Validation.MinConfidence}).Validate(ctx, rc, findings)
	if err != nil {
		return empty, err
	}
	rc.Source.Model = modelReviewAudit(rc, findings, &validation, status, warning)
	rc.Source.Findings = validation.Accepted
	rc.Source.HumanCheck = validation.HumanCheck
	rc.Source.Notes = validation.Notes
	rc.Source.Questions = validation.Questions
	rc.Source.InlineComments = nil
	for _, rejection := range validation.Rejected {
		reduced.Rejected = append(reduced.Rejected, review.ModelFindingRejection{Finding: rejection.Finding, Reason: rejection.Reason})
	}
	rc.Source.Report.Draft = renderReport(rc)
	reduced.State = review.ModelReductionValidated
	return finish()
}
