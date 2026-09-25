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
	"github.com/Y4NN777/7review/agent/tools"
)

func TestHandleChatStreamStreamsAgainstStoredRun(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	req := review.Request{Provider: "gitlab", ProjectID: "p", MRIID: 7, ChangeID: "7", Title: "Fix checkout"}
	run, err := store.Start(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(req)
	rc.Source.Report.Draft = "draft body"
	rc.Findings = []review.Finding{{ID: "F1", Severity: review.SeverityHigh, Title: "bug"}}
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("large", "small", "fake"), map[string]orchestrator.LLMProvider{
		"fake": streamingProvider{},
	})
	s := &Server{pipeline: &pipeline.Pipeline{Jobs: store, Orchestrator: orch}}

	reqHTTP := httptest.NewRequest(http.MethodPost, "/chat/stream?run=p!7", strings.NewReader(`{"message":"explain F1"}`))
	rec := httptest.NewRecorder()
	s.handleChatStream(rec, reqHTTP)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `event: done`) || !strings.Contains(out, `"delta":"stream "`) || !strings.Contains(out, `"delta":"reply"`) {
		t.Fatalf("unexpected stream response:\n%s", out)
	}
	updated, err := store.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRunEvent(updated.Events, "chat_message") || !hasRunEvent(updated.Events, "chat_response") {
		t.Fatalf("chat stream did not persist chat history: %#v", updated.Events)
	}
	if got := updated.Events[len(updated.Events)-2].Message; got != "explain F1" {
		t.Fatalf("chat message event stored wrong message %q", got)
	}
}

func TestHandleChatStreamWithoutRunStreamsOperatorChat(t *testing.T) {
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("large", "small", "fake"), map[string]orchestrator.LLMProvider{
		"fake": streamingProvider{},
	})
	s := &Server{
		cfg: &config.Config{
			Provider:       "ollama",
			ReviewModel:    "deepseek-coder-v2:16b",
			SmallModel:     "qwen2.5-coder-7b-16k:latest",
			EmbeddingModel: "nomic-embed-text:latest",
		},
		pipeline: &pipeline.Pipeline{
			Jobs:         pipeline.NewMemoryRunStore(),
			Orchestrator: orch,
			Memory:       fakeMemory{},
		},
	}

	reqHTTP := httptest.NewRequest(http.MethodPost, "/chat/stream", strings.NewReader(`{"message":"hello"}`))
	rec := httptest.NewRecorder()
	s.handleChatStream(rec, reqHTTP)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `event: done`) || !strings.Contains(out, `"delta":"stream "`) || !strings.Contains(out, `"delta":"reply"`) {
		t.Fatalf("unexpected stream response:\n%s", out)
	}
}

func TestHandleChatStreamWithoutRunAnswersRoleDeterministically(t *testing.T) {
	orch := orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("large", "small", "fake"), map[string]orchestrator.LLMProvider{
		"fake": streamingProvider{},
	})
	s := &Server{
		cfg: &config.Config{
			Provider:       "ollama",
			ReviewModel:    "deepseek-coder-v2:16b",
			SmallModel:     "qwen2.5-coder-7b-16k:latest",
			EmbeddingModel: "nomic-embed-text:latest",
		},
		pipeline: &pipeline.Pipeline{
			Jobs:         pipeline.NewMemoryRunStore(),
			Orchestrator: orch,
		},
	}

	reqHTTP := httptest.NewRequest(http.MethodPost, "/chat/stream", strings.NewReader(`{"message":"What is your role here in this system?"}`))
	rec := httptest.NewRecorder()
	s.handleChatStream(rec, reqHTTP)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	for _, want := range []string{
		"operator console assistant for 7review",
		"not currently reviewing a PR/MR",
		"`/sessions`",
		"event: done",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("deterministic operator role answer missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stream reply") {
		t.Fatalf("role question should not be routed to the model:\n%s", out)
	}
}

func TestOperatorChatSystemPromptForbidsInventedProviderWorkflows(t *testing.T) {
	prompt := operatorChatSystemPrompt()
	for _, want := range []string{
		"operator console assistant",
		"Use only the provided runtime state",
		"Do not invent runtime state",
		"do not claim to be reviewing a PR/MR",
		"Treat runtime/setup facts as operator context",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("operator prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"docker compose up --build",
		"host.docker.internal",
		"ollama",
		"qwen2.5-coder",
		"/help, /status",
	} {
		if strings.Contains(strings.ToLower(prompt), strings.ToLower(forbidden)) {
			t.Fatalf("operator system prompt should not hardcode %q:\n%s", forbidden, prompt)
		}
	}
}

func TestOperatorChatUserMessageCarriesRuntimeState(t *testing.T) {
	msg := operatorChatUserMessage(&config.Config{
		Provider:               "ollama",
		ReviewModel:            "deepseek-coder-v2:16b",
		SmallModel:             "qwen2.5-coder-7b-16k:latest",
		EmbeddingModel:         "nomic-embed-text:latest",
		OrchestratorConfigPath: "./orchestrator.yaml",
	}, "retrieval: unavailable", "hello")
	for _, want := range []string{
		"Runtime state:",
		"provider: ollama",
		"review_model: deepseek-coder-v2:16b",
		"chat_model: qwen2.5-coder-7b-16k:latest",
		"Retrieved memory:",
		"Engineer message:",
		"hello",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("operator user message missing %q:\n%s", want, msg)
		}
	}
}

func TestMemoryStoresEnableSemanticRecallForReviewAndOperator(t *testing.T) {
	cfg := &config.Config{
		MemPalaceURL:     "http://mempalace",
		MemPalaceTimeout: 5000,
		OllamaBaseURL:    "http://ollama:11434",
		EmbeddingModel:   "nomic-embed-text:latest",
	}

	reviewStore := reviewMemoryStore(cfg)
	if !reviewStore.EmbedQueries {
		t.Fatal("review recall should use query embeddings")
	}
	if !reviewStore.EmbedWrites {
		t.Fatal("review memory writes should embed approved memories")
	}

	server := &Server{cfg: cfg}
	operatorStore, ok := server.operatorMemoryStore().(*tools.MemPalaceStore)
	if !ok {
		t.Fatalf("unexpected operator memory store type %T", server.operatorMemoryStore())
	}
	if !operatorStore.EmbedQueries {
		t.Fatal("operator chat recall should use query embeddings")
	}
	if operatorStore.EmbedWrites {
		t.Fatal("operator chat should not write embeddings")
	}
}

func hasRunEvent(events []pipeline.RunEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func TestHandleChatStreamRejectsOversizedMessage(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	reqRun := review.Request{Provider: "gitlab", ProjectID: "p", MRIID: 7, ChangeID: "7", Title: "Fix checkout"}
	if _, err := store.Start(context.Background(), reqRun); err != nil {
		t.Fatal(err)
	}
	s := &Server{pipeline: &pipeline.Pipeline{Jobs: store}}
	reqHTTP := httptest.NewRequest(http.MethodPost, "/chat/stream?run=p!7", strings.NewReader(strings.Repeat("x", int(chatMaxBodyBytes)+1)))
	rec := httptest.NewRecorder()

	s.handleChatStream(rec, reqHTTP)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}
