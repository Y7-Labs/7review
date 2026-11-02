package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

type reducerProvider struct {
	response func(orchestrator.LLMRequest) string
}

func (reducerProvider) Name() string { return "reducer-test" }
func (p reducerProvider) Complete(_ context.Context, req orchestrator.LLMRequest) (string, error) {
	return p.response(req), nil
}

func TestReviewParsesEveryBatchRatherThanFirstJSONCandidate(t *testing.T) {
	provider := reducerProvider{response: func(req orchestrator.LLMRequest) string {
		id := "a"
		if strings.Contains(req.UserMessage, "b.go") {
			id = "b"
		}
		raw, _ := json.Marshal([]review.Finding{{ID: id}})
		return string(raw)
	}}
	p := &Pipeline{Config: &config.Config{MaxDiffTokens: 1}, Orchestrator: orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", provider.Name()), map[string]orchestrator.LLMProvider{provider.Name(): provider})}
	rc := review.NewContext(review.Request{})
	rc.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "a.go", Patch: "+a", TokenCount: 1}, {Path: "b.go", Patch: "+b", TokenCount: 1}}}
	findings, _, status, _, err := p.runReview(context.Background(), "run", rc)
	if err != nil || status != "parsed" || len(findings) != 2 || findings[0].ID != "a" || findings[1].ID != "b" {
		t.Fatalf("batch findings lost: status=%s findings=%#v err=%v", status, findings, err)
	}
}

func TestBatchParsingCannotHideMalformedOrMissingCoverageSlots(t *testing.T) {
	covered := `{"findings":[{"id":"a"}],"skill_coverage":[{"name":"required","status":"covered","checks":["check-a"]}]}`
	for _, bad := range []string{"", "not json"} {
		_, _, requests, status := parseReviewResponses([]string{covered, bad})
		if !isFatalModelParseStatus(status) || len(requests) != 0 {
			t.Fatalf("failed batch hidden: %s %#v", status, requests)
		}
	}
	f, c, _, status := parseReviewResponses([]string{covered, `{"findings":[{"id":"b"}]}`})
	if status != "parsed" || len(f) != 2 || len(c) != 1 || skillCoverageIsMeaningful(c[0]) {
		t.Fatalf("missing batch coverage accepted: %#v %s", c, status)
	}
}

func reducerDigest(raw []byte) string {
	s := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(s[:])
}

func recordedReducerFixture(t *testing.T, count int) []orchestrator.RecordedModelResult {
	t.Helper()
	p, req, spy, _ := canonicalPipeline(t, NewMemoryRunStore())
	prepared, err := p.PrepareService(context.Background(), req, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r, err := spy.Admit(context.Background(), prepared.Admission)
	if err != nil {
		t.Fatal(err)
	}
	s := prepared.Source
	s.Attempt = r.Attempt
	s.Execution = r.Execution
	s.Attestation = &r.Attestation
	s.Policy = r.Policy
	s.Run.ID = "reducer-run"
	rc := &review.Context{Source: s}
	ctx, err := review.WithModelCheckpoint(review.WithModelValidation(context.Background(), &review.ModelValidation{Version: 1, MinConfidence: 0.8}), rc, review.ModelCursor{Phase: review.ModelReviewBatches, BatchCount: count})
	if err != nil {
		t.Fatal(err)
	}
	var results []orchestrator.RecordedModelResult
	for index := 0; index < count; index++ {
		batchCtx, err := review.WithModelBatch(ctx, index, count)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(orchestrator.LLMRequest{Model: "pinned", MaxTokens: 20})
		step := "step5"
		if count > 1 {
			step = fmt.Sprintf("step5_batch%d", index+1)
		}
		i := orchestrator.DispatchIntent{ActionID: fmt.Sprintf("action-%d", index), Provider: "fake", Model: "pinned", Invocation: "complete", Role: "reasoner", Step: step, Job: &review.JobClaim{RunID: s.Run.ID, Token: "owner"}, Payload: payload, RequestDigest: reducerDigest(payload), Attempt: r.Attempt, Execution: r.Execution, Attestation: &r.Attestation, PolicyLimits: r.Policy.Limits, PolicyDigest: r.Policy.Digest, OperatorCeilings: *r.Policy.OperatorCeilings, ModelCheckpoint: review.ModelCheckpointFromContext(batchCtx)}
		response := []byte(`[]`)
		results = append(results, orchestrator.RecordedModelResult{Intent: i, Outcome: orchestrator.DispatchOutcome{Response: response, ResponseReceived: true, ResponseDigest: reducerDigest(response)}})
	}
	return results
}

func TestLocalModelReducerIsDeterministicAndNeverUsesCurrentProfile(t *testing.T) {
	results := recordedReducerFixture(t, 2)
	for index := range results {
		raw, _ := json.Marshal([]review.Finding{{ID: fmt.Sprintf("candidate-%d", index), FindingType: "finding", Strength: "confirmed", Severity: review.SeverityHigh, Confidence: 0.6, Title: "candidate", Description: "candidate", Location: review.Location{Path: "service.go", Line: 1}}})
		results[index].Outcome.Response = raw
		results[index].Outcome.ResponseDigest = reducerDigest(raw)
	}
	reducer := ModelReducerV1{}
	first, err := reducer.ReduceModelResults(context.Background(), results)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reducer.ReduceModelResults(context.Background(), []orchestrator.RecordedModelResult{results[1], results[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.State != review.ModelReductionValidated || len(first.RawResponses) != 2 || first.Source.Model.RejectedFindings != 2 || len(first.Source.Findings) != 0 || first.Source.Report.Draft == "" || first.Source.Report.Final != "" {
		t.Fatalf("nondeterministic or unvalidated reducer: %#v", first)
	}
}

func TestLocalModelReducerRejectsIncompleteOrContradictoryInputs(t *testing.T) {
	for _, fault := range []string{"missing batch", "duplicate slot", "response digest", "unknown response", "changed attempt", "unknown version", "unqualified validator", "missing diff", "route bound"} {
		t.Run(fault, func(t *testing.T) {
			results := recordedReducerFixture(t, 2)
			switch fault {
			case "missing batch":
				results = results[:1]
			case "duplicate slot":
				results[1] = results[0]
			case "response digest":
				results[0].Outcome.Response = []byte("different")
			case "unknown response":
				results[0].Outcome.ResponseReceived = false
			case "changed attempt":
				results[0].Intent.Attempt.AttemptID = "other"
			case "unknown version", "unqualified validator", "missing diff", "route bound":
				cp, _ := review.DecodeModelCheckpoint(results[0].Intent.ModelCheckpoint)
				if fault == "unknown version" {
					cp.Version = 2
				} else if fault == "unqualified validator" {
					cp.Validation = nil
				} else if fault == "missing diff" {
					cp.Source.Diff = nil
				} else {
					cp.Routing = &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{"reasoner": {Routes: []review.ModelRoute{{Provider: "fake", Model: "pinned"}}, MaxTokens: 21}}}
				}
				results[0].Intent.ModelCheckpoint, _ = json.Marshal(cp)
			}
			if _, err := (ModelReducerV1{}).ReduceModelResults(context.Background(), results); err == nil {
				t.Fatal("invalid inputs produced validated continuation")
			}
		})
	}
}

func TestLocalModelReducerPlansToolsAndRepairsWithoutExecutingThem(t *testing.T) {
	for _, raw := range []string{`{"tool_requests":[{"name":"get_changed_files","input":{}}]}`, `malformed response`} {
		results := recordedReducerFixture(t, 1)
		results[0].Outcome.Response = []byte(raw)
		results[0].Outcome.ResponseDigest = reducerDigest([]byte(raw))
		reduced, err := (ModelReducerV1{}).ReduceModelResults(context.Background(), results)
		if err != nil || reduced.NextCursor == nil || reduced.Source.Report.Draft != "" || reduced.Source.Report.Final != "" {
			t.Fatalf("invalid plan: %#v %v", reduced, err)
		}
		if strings.HasPrefix(raw, "{") {
			if reduced.State != review.ModelReductionNeedsTools || len(reduced.ToolRequests) != 1 || reduced.NextCursor.Round != 1 {
				t.Fatalf("tools executed or skipped: %#v", reduced)
			}
		} else if reduced.State != review.ModelReductionNeedsFindingsRepair {
			t.Fatalf("malformed output trusted: %#v", reduced)
		}
	}
}

func TestLocalModelReducerResumesTypedToolAndFormatterCursors(t *testing.T) {
	for _, phase := range []review.ModelPhase{review.ModelToolRound, review.ModelRepairFindings, review.ModelRepairCoverage} {
		t.Run(string(phase), func(t *testing.T) {
			results := recordedReducerFixture(t, 1)
			cp, _ := review.DecodeModelCheckpoint(results[0].Intent.ModelCheckpoint)
			cp.RawResponses = []string{"earlier response"}
			cp.Cursor = review.ModelCursor{Phase: phase}
			raw := `[]`
			switch phase {
			case review.ModelToolRound:
				cp.Cursor.Round = review.MaxModelToolRounds
				results[0].Intent.Step = "tool_augmented_review_round_3"
				raw = `{"findings":[],"tool_requests":[{"name":"get_changed_files"}]}`
			case review.ModelRepairFindings:
				results[0].Intent.Role = "formatter"
				results[0].Intent.Step = "repair_findings"
			case review.ModelRepairCoverage:
				results[0].Intent.Role = "formatter"
				results[0].Intent.Step = "repair_skill_coverage"
				cp.Source.Model.ParseStatus = "empty_findings"
				raw = `{"skill_coverage":[]}`
			}
			results[0].Intent.ModelCheckpoint, _ = json.Marshal(cp)
			results[0].Outcome.Response = []byte(raw)
			results[0].Outcome.ResponseDigest = reducerDigest([]byte(raw))
			reduced, err := (ModelReducerV1{}).ReduceModelResults(context.Background(), results)
			if err != nil || len(reduced.RawResponses) != 2 || reduced.Source.Report.Final != "" {
				t.Fatalf("typed resume: %#v %v", reduced, err)
			}
			if phase == review.ModelToolRound {
				if reduced.State != review.ModelReductionBlocked || reduced.NextCursor != nil || reduced.Source.Report.Draft != "" {
					t.Fatal("exhausted tool loop granted continuation")
				}
			} else if reduced.State != review.ModelReductionValidated {
				t.Fatalf("formatter result lost: %#v", reduced)
			}
		})
	}
}
