package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
