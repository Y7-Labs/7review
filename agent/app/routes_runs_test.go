package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestRoutesDisableUnconfiguredProviderWebhooks(t *testing.T) {
	s := &Server{
		cfg:  &config.Config{GitHubAPIURL: "https://api.github.com", GitHubToken: "token", GitHubWebhookSecret: "github-secret"},
		mux:  http.NewServeMux(),
		work: make(chan workItem, 1),
		seen: make(map[string]time.Time),
	}
	s.routes()
	req := httptest.NewRequest(http.MethodPost, "/webhook/gitlab", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected inactive GitLab route to return 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 0 {
		t.Fatal("inactive webhook should not enqueue work")
	}
}

func TestRoutesEnableOnlyConfiguredGitHubWebhook(t *testing.T) {
	body := `{
		"action":"opened",
		"number":7,
		"repository":{"full_name":"o/r"},
		"pull_request":{"title":"Fix","head":{"sha":"abc"},"base":{"sha":"def"}}
	}`
	s := &Server{
		cfg:      &config.Config{GitHubAPIURL: "https://api.github.com", GitHubToken: "token", GitHubWebhookSecret: "github-secret", WebhookReviewMode: "auto"},
		pipeline: &pipeline.Pipeline{},
		mux:      http.NewServeMux(),
		work:     make(chan workItem, 1),
		seen:     make(map[string]time.Time),
	}
	s.routes()
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", githubSignature("github-secret", body))
	rec := httptest.NewRecorder()

	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected configured GitHub route to accept, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 1 {
		t.Fatal("configured GitHub route should enqueue work")
	}
}

func TestHandleRunEndpointsExposeStoredReviewContext(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	req := review.Request{Provider: "gitlab", ProjectID: "p", MRIID: 7, ChangeID: "7", Title: "Fix checkout"}
	run, err := store.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(req)
	rc.Source.Report.Draft = "draft body"
	rc.Findings = []review.Finding{{ID: "F1", Severity: review.SeverityHigh, Title: "bug"}}
	rc.Source.Findings = rc.Findings
	rc.Source.HumanCheck = []review.Finding{{ID: "H1", Severity: review.SeverityMedium, Title: "needs check", ValidationStatus: "needs_human_check"}}
	rc.Source.Notes = []review.Finding{{ID: "N1", Severity: review.SeverityInfo, Title: "note", FindingType: "note"}}
	rc.Source.Questions = []review.Finding{{ID: "Q1", Severity: review.SeverityInfo, Title: "question", FindingType: "question"}}
	rc.Source.Attestation = &review.SnapshotAttestation{Snapshot: review.SnapshotIdentity{RepositoryID: "p", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)}, Verified: true}
	rc.Source.Policy = review.PolicyProjection{Mode: "enforce", Digest: "sha256:" + strings.Repeat("b", 64), SourceRevision: "base", TriggerAccepted: true}
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), run.ID, pipeline.StatusDrafted, nil); err != nil {
		t.Fatal(err)
	}
	s := &Server{pipeline: &pipeline.Pipeline{Jobs: store}}

	listReq := httptest.NewRequest(http.MethodGet, "/runs", nil)
	listRec := httptest.NewRecorder()
	s.handleRuns(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	var listed []runDTO
	if err := json.NewDecoder(listRec.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "p!7" || len(listed[0].Findings) != 0 || listed[0].EventCount == 0 || len(listed[0].Events) != 0 {
		t.Fatalf("unexpected list response: %#v", listed)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/run?id=p!7", nil)
	getRec := httptest.NewRecorder()
	s.handleRun(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected get 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var detail runDTO
	if err := json.NewDecoder(getRec.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.DraftReport != "draft body" || len(detail.Findings) != 1 || detail.EventCount == 0 || len(detail.Events) != detail.EventCount {
		t.Fatalf("unexpected detail response: %#v", detail)
	}
	if len(detail.HumanCheck) != 1 || len(detail.Notes) != 1 || len(detail.Questions) != 1 {
		t.Fatalf("detail response missing review quality categories: %#v", detail)
	}
	if detail.Snapshot == nil || detail.Snapshot.HeadRevision != "head" || detail.Policy == nil || detail.Policy.Mode != "enforce" {
		t.Fatalf("detail response missing trusted input projection: %#v", detail)
	}
	if detail.Events[0].Type != "run_started" {
		t.Fatalf("detail response missing run timeline: %#v", detail.Events)
	}
}
