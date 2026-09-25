package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/config"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestHandleToolExecuteObservabilityTools(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	reqRun := review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "9", Title: "Update API"}
	run, err := store.Start(context.Background(), reqRun)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(reqRun)
	rc.Source.SCM = &review.SCMContext{
		Provider:    "github",
		ProjectID:   "owner/repo",
		ChangeID:    "9",
		WebURL:      "https://github.example.com/owner/repo/pull/9",
		Discussions: []review.Discussion{{ID: "d1"}},
		Checks:      []review.CheckRun{{Name: "ci", Status: "completed"}},
		Approvals:   []review.Approval{{Reviewer: "lead", State: "approved"}},
	}
	rc.Source.ChangedFiles = []review.ChangedFile{{
		NewPath:   "api/users.go",
		Patch:     "@@ -1 +1\n+change",
		Status:    "modified",
		Additions: 3,
		Deletions: 1,
	}}
	rc.Diff = &review.StructuredDiff{Files: []review.FileDiff{{
		Path:       "api/users.go",
		Patch:      "@@ -1 +1\n+change",
		TokenCount: 42,
	}}}
	rc.Source.Diff = rc.Diff
	rc.CorpusSections = []review.Section{{Path: "PRD.md", Title: "PRD", Kind: review.KindPlanning, Content: "feature\nrule"}}
	rc.Source.CorpusSections = rc.CorpusSections
	rc.Source.Evidence = []review.EvidenceItem{{
		Source:          "PRD.md",
		HeadingOrKey:    "PRD",
		Kind:            review.KindPlanning,
		Authority:       "requirements",
		MatchedSignals:  []string{"FR-1"},
		SelectionReason: "requirements: identifier FR-1",
		Score:           26,
		ContentBytes:    len("feature\nrule"),
	}}
	rc.SkillSections = []review.Section{{Path: "agent/skills/api-contract-review/SKILL.md", Title: "api-contract-review", Kind: review.KindRules, Content: "skill body"}}
	rc.Source.SkillSections = rc.SkillSections
	rc.Source.Memory = review.MemoryRecall{Conventions: []string{"return typed errors"}}
	rc.Source.Report.Draft = "draft report"
	rc.Source.Report.Final = "final report"
	rc.HILApproved = true
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), run.ID, pipeline.StatusFinalized, nil); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		cfg: &config.Config{
			Provider:          "openrouter",
			ReviewModel:       "openai/gpt-4o",
			SmallModel:        "openai/gpt-4o-mini",
			OpenRouterAPIKey:  "secret",
			OpenRouterBaseURL: "https://openrouter.ai/api",
			DeepSeekBaseURL:   "https://api.deepseek.com",
		},
		pipeline: &pipeline.Pipeline{Jobs: store, Memory: proposalMemory{}},
	}

	cases := []struct {
		name string
		body string
		want []string
	}{
		{name: "get_selected_context", body: `{"name":"get_selected_context","input":{"run":"owner/repo!9"}}`, want: []string{`"corpus_sections"`, `"evidence_manifest"`, `"selection_reason":"requirements: identifier FR-1"`, `"PRD.md"`, `"skill_sections"`, `"return typed errors"`}},
		{name: "get_diff_summary", body: `{"name":"get_diff_summary","input":{"run":"owner/repo!9"}}`, want: []string{`"total_tokens":42`, `"additions":3`, `"deletions":1`, `"api/users.go"`}},
		{name: "get_publish_status", body: `{"name":"get_publish_status","input":{"run":"owner/repo!9"}}`, want: []string{`"status":"finalized"`, `"hil_approved":true`, `"has_final_report":true`, `"scm_discussions":1`}},
		{name: "list_provider_status", body: `{"name":"list_provider_status"}`, want: []string{`"active_provider":"openrouter"`, `"name":"openrouter"`, `"configured":true`, `"reasoner"`}},
		{name: "preview_memory_proposal", body: `{"name":"preview_memory_proposal","input":{"run":"owner/repo!9"}}`, want: []string{`"approved":true`, `"Conventions":["final report"]`, `"final_bytes":12`}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/tools/execute", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			s.handleToolExecute(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("response missing %q:\n%s", want, rec.Body.String())
				}
			}
		})
	}
}

func TestHandleToolExecuteProviderStatusUsesLoadedOrchestrator(t *testing.T) {
	orch := orchestrator.NewOrchestrator(&orchestrator.OrchestratorConfig{
		Roles: map[orchestrator.ModelRole]orchestrator.RoleConfig{
			orchestrator.RoleReasoner: {
				Primary:   orchestrator.ModelSpec{Model: "claude-sonnet", Provider: "anthropic"},
				Fallbacks: []orchestrator.ModelSpec{{Model: "qwen2.5-coder-7b-16k:latest", Provider: "ollama"}},
				MaxTokens: 4096,
				Parallel:  true,
			},
		},
	}, map[string]orchestrator.LLMProvider{"ollama": staticResponseProvider{response: "ok"}})
	s := &Server{
		cfg: &config.Config{
			Provider:               "anthropic",
			OrchestratorConfigPath: "/app/orchestrator.yaml",
			OllamaBaseURL:          "http://ollama:11434",
		},
		pipeline: &pipeline.Pipeline{Jobs: pipeline.NewMemoryRunStore(), Orchestrator: orch},
	}
	req := httptest.NewRequest(http.MethodPost, "/tools/execute", strings.NewReader(`{"name":"list_provider_status"}`))
	rec := httptest.NewRecorder()

	s.handleToolExecute(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"mode":"orchestrator"`, `"active_provider":""`, `"primary":"claude-sonnet@anthropic"`, `"fallbacks":["qwen2.5-coder-7b-16k:latest@ollama"]`, `"name":"ollama"`, `"configured":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("provider status missing %q:\n%s", want, body)
		}
	}
}
