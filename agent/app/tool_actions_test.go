package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestHandleToolExecuteSuppressFinding(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	reqRun := review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"}
	run, err := store.Start(context.Background(), reqRun)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(reqRun)
	rc.Findings = []review.Finding{
		{ID: "F1", Severity: review.SeverityHigh, Title: "Keep", Confidence: 0.9},
		{ID: "F2", Severity: review.SeverityLow, Title: "Suppress", Confidence: 0.8},
	}
	rc.Source.Findings = rc.Findings
	rc.Source.Report.Draft = "draft with Suppress"
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), run.ID, pipeline.StatusDrafted, nil); err != nil {
		t.Fatal(err)
	}
	s := &Server{pipeline: &pipeline.Pipeline{Jobs: store}}
	body := `{"name":"suppress_finding","input":{"run":"owner/repo!7","finding_id":"F2","reason":"false positive"}}`
	req := httptest.NewRequest(http.MethodPost, "/tools/execute", strings.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleToolExecute(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := store.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Findings) != 1 || updated.Findings[0].ID != "F1" {
		t.Fatalf("finding was not suppressed: %#v", updated.Findings)
	}
	if updated.Context == nil || len(updated.Context.HILRejectedIDs) != 1 || updated.Context.HILRejectedIDs[0] != "F2" {
		t.Fatalf("suppression was not persisted: %#v", updated.Context)
	}
	if !strings.Contains(rec.Body.String(), `"accepted":true`) || !strings.Contains(rec.Body.String(), `"finding_id":"F2"`) {
		t.Fatalf("unexpected suppress response: %s", rec.Body.String())
	}
}

func TestHandleToolExecuteReviseDraft(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	reqRun := review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"}
	run, err := store.Start(context.Background(), reqRun)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(reqRun)
	rc.Source.Report.Draft = "old draft"
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), run.ID, pipeline.StatusDrafted, nil); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("large", "small", "fake"), map[string]orchestrator.LLMProvider{
		"fake": staticResponseProvider{response: "new draft"},
	})
	s := &Server{pipeline: &pipeline.Pipeline{Jobs: store, Orchestrator: orch}}
	body := `{"name":"revise_draft","input":{"run":"owner/repo!7","request":"tighten wording"}}`
	req := httptest.NewRequest(http.MethodPost, "/tools/execute", strings.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleToolExecute(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := store.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DraftReport != "new draft" || updated.Context.Source.Report.Draft != "new draft" {
		t.Fatalf("draft was not revised: %#v", updated)
	}
}

func TestHandleToolExecuteRerunReview(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	reqRun := review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7", Title: "Fix retry"}
	run, err := store.Start(context.Background(), reqRun)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(reqRun)
	rc.Source.Report.Draft = "old draft"
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), run.ID, pipeline.StatusFailed, errors.New("old failure")); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("large", "small", "fake"), map[string]orchestrator.LLMProvider{
		"fake": staticResponseProvider{response: `[]`},
	})
	s := &Server{pipeline: &pipeline.Pipeline{
		Config:           &config.Config{MaxDiffTokens: 6000, CorpusRoot: t.TempDir()},
		Jobs:             store,
		Orchestrator:     orch,
		SCM:              fakeAppSCM{},
		SCMPublisher:     &fakeAppPublisher{},
		Memory:           fakeMemory{},
		ContextReducer:   fakeReducer{},
		Policy:           pipeline.DefaultPolicyFilter{},
		FindingValidator: pipeline.DefaultFindingValidator{},
	}}
	body := `{"name":"rerun_review","input":{"run":"owner/repo!7","reason":"new commits"}}`
	req := httptest.NewRequest(http.MethodPost, "/tools/execute", strings.NewReader(body))
	rec := httptest.NewRecorder()

	s.handleToolExecute(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, err := store.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != pipeline.StatusDrafted || updated.Error != "" || !strings.Contains(updated.DraftReport, "No validated findings") {
		t.Fatalf("run was not rerun: %#v", updated)
	}
}
