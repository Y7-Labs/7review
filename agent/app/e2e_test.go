package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestE2EGitHubReviewLifecycle(t *testing.T) {
	s, publisher, memory := newE2EServer(t, &config.Config{
		APIToken:               "operator-token",
		GitHubAPIURL:           "https://api.github.com",
		GitHubToken:            "github-token",
		GitHubWebhookSecret:    "github-secret",
		Provider:               "openrouter",
		ReviewModel:            "review",
		SmallModel:             "small",
		OpenRouterAPIKey:       "model-token",
		OpenRouterBaseURL:      "https://openrouter.ai/api",
		HeadroomURL:            "http://headroom",
		MemPalaceURL:           "http://mempalace",
		WebhookWorkers:         1,
		WebhookQueueSize:       4,
		WebhookJobTimeout:      1000,
		WebhookReviewMode:      "auto",
		MaxDiffTokens:          6000,
		CorpusRoot:             t.TempDir(),
		ReadHeaderTimeout:      5000,
		ReadTimeout:            30000,
		WriteTimeout:           30000,
		IdleTimeout:            30000,
		HeadroomTimeout:        5000,
		MemPalaceTimeout:       5000,
		OrchestratorConfigPath: "",
	})
	body := `{
		"action":"opened",
		"number":7,
		"repository":{"full_name":"owner/repo"},
		"pull_request":{
			"title":"Fix auth",
			"body":"body",
			"html_url":"https://github.example.com/owner/repo/pull/7",
			"user":{"login":"alice"},
			"head":{"ref":"feature","sha":"abc"},
			"base":{"ref":"main","sha":"def"},
			"labels":[{"name":"security"}]
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "e2e-github")
	req.Header.Set("X-Hub-Signature-256", githubSignature("github-secret", body))

	runWebhookAndQueuedWork(t, s, req)
	assertE2ERunLifecycle(t, s, publisher, memory, "owner/repo!7", "github")
}

func TestE2EGitLabReviewLifecycle(t *testing.T) {
	s, publisher, memory := newE2EServer(t, &config.Config{
		APIToken:          "operator-token",
		GitLabURL:         "https://gitlab.example.com",
		GitLabToken:       "gitlab-token",
		WebhookSecret:     "gitlab-secret",
		Provider:          "deepseek",
		ProviderAPIKey:    "model-token",
		ReviewModel:       "review",
		SmallModel:        "small",
		HeadroomURL:       "http://headroom",
		MemPalaceURL:      "http://mempalace",
		WebhookWorkers:    1,
		WebhookQueueSize:  4,
		WebhookJobTimeout: 1000,
		WebhookReviewMode: "auto",
		MaxDiffTokens:     6000,
		CorpusRoot:        t.TempDir(),
		HeadroomTimeout:   5000,
		MemPalaceTimeout:  5000,
	})
	body := `{
		"object_kind":"merge_request",
		"event_type":"merge_request",
		"project":{"id":42},
		"object_attributes":{
			"iid":7,
			"action":"update",
			"title":"Fix auth",
			"description":"body",
			"url":"https://gitlab.example.com/p/-/merge_requests/7",
			"source_branch":"feature",
			"target_branch":"main",
			"last_commit":{"id":"abc"},
			"labels":["security"]
		},
		"user":{"username":"alice"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/gitlab", strings.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "gitlab-secret")
	req.Header.Set("X-Gitlab-Event-UUID", "e2e-gitlab")

	runWebhookAndQueuedWork(t, s, req)
	assertE2ERunLifecycle(t, s, publisher, memory, "42!7", "gitlab")
}

func newE2EServer(t *testing.T, cfg *config.Config) (*Server, *e2ePublisher, *e2eMemory) {
	t.Helper()
	store := pipeline.NewMemoryRunStore()
	model := e2eModelProvider{
		review: `[{
			"ID":"F1",
			"Severity":"high",
			"Title":"Missing auth guard",
			"Description":"The changed handler trusts user input without verifying authorization.",
			"Suggestion":"Check authorization before processing the request.",
			"Location":{"Path":"main.go","Line":10},
			"Confidence":0.94
		}]`,
		revision: "## 7review Draft\n\nRevised after engineer request.\n",
	}
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("review", "small", "fake"), map[string]orchestrator.LLMProvider{
		"fake": model,
	})
	publisher := &e2ePublisher{}
	memory := &e2eMemory{}
	s := &Server{
		cfg: cfg,
		pipeline: &pipeline.Pipeline{
			Config:           cfg,
			Orchestrator:     orch,
			Jobs:             store,
			SCM:              e2eSCM{},
			SCMPublisher:     publisher,
			Memory:           memory,
			ContextReducer:   fakeReducer{},
			Policy:           pipeline.DefaultPolicyFilter{},
			FindingValidator: pipeline.DefaultFindingValidator{},
		},
		mux:  http.NewServeMux(),
		work: make(chan workItem, cfg.WebhookQueueSize),
		seen: make(map[string]time.Time),
	}
	s.routes()
	return s, publisher, memory
}

func runWebhookAndQueuedWork(t *testing.T, s *Server, req *http.Request) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected webhook 202, got %d: %s", rec.Code, rec.Body.String())
	}
	select {
	case item := <-s.work:
		if err := s.runWorkItem(1, item); err != nil {
			t.Fatalf("queued webhook work failed: %v", err)
		}
	default:
		t.Fatal("webhook did not enqueue review work")
	}
}

func assertE2ERunLifecycle(t *testing.T, s *Server, publisher *e2ePublisher, memory *e2eMemory, runID string, provider string) {
	t.Helper()
	run := toolExecute(t, s, `{"name":"get_run","input":{"id":`+quote(runID)+`}}`)
	for _, want := range []string{`"status":"drafted"`, `"draft_report"`, `"Missing auth guard"`, `"provider":"` + provider + `"`} {
		if !strings.Contains(run, want) {
			t.Fatalf("get_run missing %q:\n%s", want, run)
		}
	}
	if !strings.Contains(publisher.draftReport, "Missing auth guard") || publisher.draftSource == nil || publisher.draftSource.Provider != provider {
		t.Fatalf("draft was not published through provider source: %#v", publisher)
	}
	assertCharacterizedDraftArtifacts(t, s, runID, provider)

	selected := toolExecute(t, s, `{"name":"get_selected_context","input":{"run":`+quote(runID)+`}}`)
	if !strings.Contains(selected, `"memory"`) || !strings.Contains(selected, "durable convention") {
		t.Fatalf("selected context did not include recalled memory:\n%s", selected)
	}
	diff := toolExecute(t, s, `{"name":"get_diff_summary","input":{"run":`+quote(runID)+`}}`)
	if !strings.Contains(diff, `"main.go"`) || !strings.Contains(diff, `"total_tokens"`) {
		t.Fatalf("diff summary missing normalized file:\n%s", diff)
	}

	chatReq := httptest.NewRequest(http.MethodPost, "/chat/stream?run="+runID, strings.NewReader(`{"message":"explain F1"}`))
	addOperatorAuth(chatReq)
	chatRec := httptest.NewRecorder()
	s.mux.ServeHTTP(chatRec, chatReq)
	if chatRec.Code != http.StatusOK || !strings.Contains(chatRec.Body.String(), `event: done`) || !strings.Contains(chatRec.Body.String(), "streamed answer") {
		t.Fatalf("chat stream failed code=%d:\n%s", chatRec.Code, chatRec.Body.String())
	}

	suppress := toolExecute(t, s, `{"name":"suppress_finding","input":{"run":`+quote(runID)+`,"finding_id":"F1","reason":"covered by existing guard"}}`)
	if !strings.Contains(suppress, `"accepted":true`) {
		t.Fatalf("suppress_finding failed:\n%s", suppress)
	}
	afterSuppress := toolExecute(t, s, `{"name":"get_run","input":{"id":`+quote(runID)+`}}`)
	if strings.Contains(afterSuppress, "Missing auth guard") || !strings.Contains(afterSuppress, "No validated findings") {
		t.Fatalf("suppressed finding still visible or report not regenerated:\n%s", afterSuppress)
	}

	revised := toolExecute(t, s, `{"name":"revise_draft","input":{"run":`+quote(runID)+`,"request":"tighten wording"}}`)
	if !strings.Contains(revised, `"accepted":true`) {
		t.Fatalf("revise_draft failed:\n%s", revised)
	}
	afterRevision := toolExecute(t, s, `{"name":"get_run","input":{"id":`+quote(runID)+`}}`)
	if !strings.Contains(afterRevision, "Revised after engineer request") {
		t.Fatalf("revised draft not persisted:\n%s", afterRevision)
	}

	final := "## 7review Final\n\nApproved final."
	approved := toolExecute(t, s, `{"name":"approve_run","input":{"run":`+quote(runID)+`,"report":`+quote(final)+`}}`)
	if !strings.Contains(approved, `"accepted":true`) {
		t.Fatalf("approve_run failed:\n%s", approved)
	}
	finalized := toolExecute(t, s, `{"name":"get_publish_status","input":{"run":`+quote(runID)+`}}`)
	if !strings.Contains(finalized, `"status":"finalized"`) || !strings.Contains(finalized, `"has_final_report":true`) {
		t.Fatalf("publish status not finalized:\n%s", finalized)
	}
	if publisher.finalReport != final || memory.writes != 1 {
		t.Fatalf("final publish/memory write did not happen: publisher=%#v memory=%#v", publisher, memory)
	}

	proposal := toolExecute(t, s, `{"name":"preview_memory_proposal","input":{"run":`+quote(runID)+`}}`)
	if !strings.Contains(proposal, "Approved final") {
		t.Fatalf("memory preview missing final report:\n%s", proposal)
	}

	rerun := toolExecute(t, s, `{"name":"rerun_review","input":{"run":`+quote(runID)+`,"reason":"new commit"}}`)
	if !strings.Contains(rerun, `"accepted":true`) {
		t.Fatalf("rerun_review failed:\n%s", rerun)
	}
	rerunStatus := toolExecute(t, s, `{"name":"get_publish_status","input":{"run":`+quote(runID)+`}}`)
	if !strings.Contains(rerunStatus, `"status":"drafted"`) {
		t.Fatalf("rerun did not return to drafted state:\n%s", rerunStatus)
	}
}

type characterizedDraftArtifacts struct {
	Status           pipeline.RunStatus     `json:"status"`
	Request          review.Request         `json:"request"`
	SCM              *review.SCMContext     `json:"scm"`
	ChangedFiles     []review.ChangedFile   `json:"changed_files"`
	Diff             *review.StructuredDiff `json:"diff"`
	Memory           review.MemoryRecall    `json:"memory"`
	Findings         []review.Finding       `json:"findings"`
	HumanCheck       []review.Finding       `json:"human_check"`
	Notes            []review.Finding       `json:"notes"`
	Questions        []review.Finding       `json:"questions"`
	InlineComments   []review.InlineComment `json:"inline_comments"`
	ModelStatus      string                 `json:"model_status"`
	ParsedFindings   int                    `json:"parsed_findings"`
	AcceptedFindings int                    `json:"accepted_findings"`
	HasDraftReport   bool                   `json:"has_draft_report"`
	HasFinalReport   bool                   `json:"has_final_report"`
	HILApproved      bool                   `json:"hil_approved"`
	EventTypes       []string               `json:"event_types"`
}

func assertCharacterizedDraftArtifacts(t *testing.T, s *Server, runID string, provider string) {
	t.Helper()
	run, err := s.pipeline.Jobs.Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Source == nil {
		t.Fatal("characterization requires persisted review source")
	}
	snapshot := characterizedDraftArtifacts{
		Status:           run.Status,
		Request:          run.Request,
		SCM:              run.Source.SCM,
		ChangedFiles:     run.Source.ChangedFiles,
		Diff:             run.Source.Diff,
		Memory:           run.Source.Memory,
		Findings:         run.Source.Findings,
		HumanCheck:       run.Source.HumanCheck,
		Notes:            run.Source.Notes,
		Questions:        run.Source.Questions,
		InlineComments:   run.Source.InlineComments,
		ModelStatus:      run.Source.Model.ParseStatus,
		ParsedFindings:   run.Source.Model.ParsedFindings,
		AcceptedFindings: run.Source.Model.AcceptedFindings,
		HasDraftReport:   strings.TrimSpace(run.DraftReport) != "",
		HasFinalReport:   strings.TrimSpace(run.FinalReport) != "",
		HILApproved:      run.HILApproved,
	}
	for _, event := range run.Events {
		snapshot.EventTypes = append(snapshot.EventTypes, event.Type)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join("testdata", "lifecycle_"+provider+".json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(want) {
		t.Fatalf("%s lifecycle artifacts changed; run UPDATE_GOLDEN=1 go test ./agent/app after verifying the behavior\n--- got ---\n%s\n--- want ---\n%s", provider, data, want)
	}
}

func toolExecute(t *testing.T, s *Server, body string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/tools/execute", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	addOperatorAuth(req)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tool execute failed code=%d body=%s request=%s", rec.Code, rec.Body.String(), body)
	}
	return rec.Body.String()
}

func addOperatorAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer operator-token")
	req.Header.Set("X-7review-Token", "operator-token")
}

func quote(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

type e2eSCM struct{}

func (e2eSCM) Enrich(_ context.Context, req review.Request) (*review.SCMContext, error) {
	return &review.SCMContext{
		Provider:    req.Provider,
		ProjectID:   req.ProjectID,
		Repository:  req.Repository,
		ChangeID:    req.ChangeID,
		MRIID:       req.MRIID,
		Title:       req.Title,
		Description: req.Description,
		Author:      req.Author,
		WebURL:      req.WebURL,
		Labels:      append([]string(nil), req.Labels...),
		DiffRefs: review.DiffRefs{
			BaseSHA: req.TargetSHA,
			HeadSHA: req.SourceSHA,
		},
		Files: []review.ChangedFile{{
			OldPath:   "main.go",
			NewPath:   "main.go",
			Patch:     "@@ -10,3 +10,4 @@\n+handleWithoutAuth(userInput)",
			Status:    "modified",
			Additions: 1,
			Deletions: 0,
		}},
		Checks:      []review.CheckRun{{Name: "ci", Status: "completed", Conclusion: "success"}},
		Discussions: []review.Discussion{{ID: "d1", Author: "reviewer", Body: "please check auth"}},
	}, nil
}

type e2ePublisher struct {
	draftSource *review.SCMContext
	draftReport string
	finalSource *review.SCMContext
	finalReport string
}

func (p *e2ePublisher) PublishDraft(_ context.Context, source *review.SCMContext, report string) error {
	p.draftSource = source
	p.draftReport = report
	return nil
}

func (p *e2ePublisher) PublishFinal(_ context.Context, source *review.SCMContext, report string) error {
	p.finalSource = source
	p.finalReport = report
	return nil
}

type e2eMemory struct {
	writes int
}

func (m *e2eMemory) Recall(context.Context, review.Request) (pipeline.Recall, error) {
	return pipeline.Recall{Conventions: []string{"durable convention: require auth guards"}}, nil
}

func (m *e2eMemory) ProposeUpdate(_ context.Context, rc *review.Context) (pipeline.UpdateProposal, error) {
	if rc == nil || !rc.HILApproved {
		return pipeline.UpdateProposal{}, fmt.Errorf("approval required")
	}
	return pipeline.UpdateProposal{Conventions: []string{rc.Source.Report.Final}}, nil
}

func (m *e2eMemory) Write(context.Context, pipeline.UpdateProposal) error {
	m.writes++
	return nil
}

func (m *e2eMemory) Check(context.Context) error {
	return nil
}

type e2eModelProvider struct {
	review   string
	revision string
}

func (p e2eModelProvider) Name() string { return "fake" }

func (p e2eModelProvider) Complete(_ context.Context, req orchestrator.LLMRequest) (string, error) {
	if strings.Contains(req.UserMessage, "Engineer revision request") {
		return p.revision, nil
	}
	return p.review, nil
}

func (p e2eModelProvider) Stream(_ context.Context, _ orchestrator.LLMRequest, emit orchestrator.StreamHandler) error {
	return emit("streamed answer")
}
