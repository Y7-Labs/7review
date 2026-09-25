package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestReviewPolicyManualFirstRequiresIncludeLabel(t *testing.T) {
	s := &Server{cfg: &config.Config{WebhookReviewMode: "manual_first", ReviewLabelInclude: []string{"7review"}}}

	if decision := s.reviewPolicyDecision(review.Request{Labels: []string{"backend"}}); decision.allowed {
		t.Fatalf("expected unlabeled webhook to be ignored: %#v", decision)
	}
	if decision := s.reviewPolicyDecision(review.Request{Labels: []string{"7review"}}); !decision.allowed {
		t.Fatalf("expected include label to allow webhook: %#v", decision)
	}
}

func TestReviewPolicyExcludeWinsOverInclude(t *testing.T) {
	s := &Server{cfg: &config.Config{
		WebhookReviewMode:  "manual_first",
		ReviewLabelInclude: []string{"7review"},
		ReviewLabelExclude: []string{"no-review"},
	}}

	decision := s.reviewPolicyDecision(review.Request{Labels: []string{"7review", "no-review"}})
	if decision.allowed || !strings.Contains(decision.reason, "excluded label") {
		t.Fatalf("expected exclude label to win: %#v", decision)
	}
}

func TestReviewPolicyAutoAndOffModes(t *testing.T) {
	auto := &Server{cfg: &config.Config{WebhookReviewMode: "auto"}}
	if decision := auto.reviewPolicyDecision(review.Request{}); !decision.allowed {
		t.Fatalf("expected auto mode to allow by default: %#v", decision)
	}
	off := &Server{cfg: &config.Config{WebhookReviewMode: "off"}}
	if decision := off.reviewPolicyDecision(review.Request{Labels: []string{"7review"}}); decision.allowed {
		t.Fatalf("expected off mode to ignore: %#v", decision)
	}
}

func TestReviewPolicyV2EnforceDefersPositiveSelectionButKeepsAbsoluteExcludes(t *testing.T) {
	s := &Server{cfg: &config.Config{
		WebhookReviewMode: "manual_first", PolicyV2Mode: "enforce",
		ReviewLabelInclude: []string{"legacy-label"}, ReviewLabelExclude: []string{"no-review"},
	}}
	if decision := s.reviewPolicyDecision(review.Request{Labels: []string{"backend"}}); !decision.allowed || !strings.Contains(decision.reason, "deferred") {
		t.Fatalf("V2 enforcement should defer positive trigger selection: %#v", decision)
	}
	if decision := s.reviewPolicyDecision(review.Request{Labels: []string{"backend", "no-review"}}); decision.allowed {
		t.Fatalf("runtime exclusion must remain absolute: %#v", decision)
	}
}

func TestReviewPolicyDisallowedProjectRepoAndBranch(t *testing.T) {
	s := &Server{cfg: &config.Config{
		WebhookReviewMode:     "auto",
		ReviewAllowedProjects: []string{"25"},
		ReviewAllowedRepos:    []string{"owner/repo"},
		ReviewBranchInclude:   []string{"main"},
	}}
	if decision := s.reviewPolicyDecision(review.Request{ProjectID: "26", Repository: "owner/repo", TargetBranch: "main"}); decision.allowed {
		t.Fatalf("expected project policy to ignore: %#v", decision)
	}
	if decision := s.reviewPolicyDecision(review.Request{ProjectID: "25", Repository: "other/repo", TargetBranch: "main"}); decision.allowed {
		t.Fatalf("expected repo policy to ignore: %#v", decision)
	}
	if decision := s.reviewPolicyDecision(review.Request{ProjectID: "25", Repository: "owner/repo", TargetBranch: "develop"}); decision.allowed {
		t.Fatalf("expected branch policy to ignore: %#v", decision)
	}
}

func TestHandleToolExecuteRequestReviewEnqueuesGitLab(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	s := &Server{
		pipeline: &pipeline.Pipeline{Jobs: store},
		work:     make(chan workItem, 1),
		active:   make(map[string]bool),
	}
	req := httptest.NewRequest(http.MethodPost, "/tools/execute", strings.NewReader(`{"name":"request_review","input":{"provider":"gitlab","project_id":"25","mr_iid":19}}`))
	rec := httptest.NewRecorder()

	s.handleToolExecute(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Result requestReviewResult `json:"result"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Result.RunID != "25!19" || envelope.Result.Status != "enqueued" {
		t.Fatalf("unexpected request_review result: %#v", envelope.Result)
	}
	item := <-s.work
	if item.name != "gitlab/25/19" {
		t.Fatalf("unexpected work item name %q", item.name)
	}
}

func TestHandleToolExecuteRequestReviewRejectsActiveDuplicate(t *testing.T) {
	s := &Server{
		pipeline: &pipeline.Pipeline{Jobs: pipeline.NewMemoryRunStore()},
		work:     make(chan workItem, 1),
		active:   map[string]bool{"owner/repo!7": true},
	}
	req := httptest.NewRequest(http.MethodPost, "/tools/execute", strings.NewReader(`{"name":"request_review","input":{"provider":"github","repository":"owner/repo","pr_number":7}}`))
	rec := httptest.NewRecorder()

	s.handleToolExecute(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already running") {
		t.Fatalf("expected already-running message, got %s", rec.Body.String())
	}
}

func TestRequestReviewQueueFullReturnsClearError(t *testing.T) {
	s := &Server{
		pipeline: &pipeline.Pipeline{Jobs: pipeline.NewMemoryRunStore()},
		work:     make(chan workItem),
		active:   make(map[string]bool),
	}
	_, err := s.requestReview(context.Background(), review.Request{Provider: "github", ProjectID: "owner/repo", Repository: "owner/repo", MRIID: 7, ChangeID: "7"})
	if err == nil || !strings.Contains(err.Error(), "review queue is full") {
		t.Fatalf("expected queue full error, got %v", err)
	}
}
