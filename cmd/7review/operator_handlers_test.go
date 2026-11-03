package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/tools"
	"github.com/Y4NN777/7review/agent/ui"
)

func TestChatDispositionFindingUsesCanonicalReadThenCommand(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	view := cliFindingView(t)
	viewRaw, _ := json.Marshal(view)
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return jsonResponse(http.StatusOK, string(viewRaw)), nil
		}
		raw, _ := io.ReadAll(req.Body)
		command, err := review.DecodeFindingDispositionCommand(raw)
		if err != nil || command.Payload.FindingDigest != view.FindingDigest || command.ExpectedVersion != view.ControlVersion {
			t.Fatalf("chat changed finding authority: %+v %v", command, err)
		}
		return jsonResponse(http.StatusOK, cliFindingReceipt(command)), nil
	})}
	var out strings.Builder
	text := "/disposition-finding attempt-1 F-1 --command-id finding-chat --decision disputed --reason 'the cited branch is unreachable'"
	handled, err := chatCommandHandlerWithClient("https://agent", "", client)(context.Background(), text, &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil || !handled || calls != 2 || !strings.Contains(out.String(), "disputed finding F-1") {
		t.Fatalf("chat disposition: handled=%t calls=%d out=%q err=%v", handled, calls, out.String(), err)
	}
}

func TestChatCommandHandlerRendersRunHistory(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/run" || req.URL.Query().Get("id") != "owner/repo!7" {
			t.Fatalf("unexpected request: %s", req.URL.String())
		}
		return jsonResponse(http.StatusOK, `{"id":"owner/repo!7","status":"drafted","title":"Fix validation","events":[{"type":"chat_message","message":"first"},{"type":"chat_message","message":"second"},{"type":"status_changed","message":"drafted"}]}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), "/history chat_message 1", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected command to be handled")
	}
	if !strings.Contains(out.String(), "history 1/3 events") || !strings.Contains(out.String(), "second") {
		t.Fatalf("history command output missing filtered event:\n%s", out.String())
	}
	if strings.Contains(out.String(), "first") || strings.Contains(out.String(), "status_changed") {
		t.Fatalf("history command output included excluded events:\n%s", out.String())
	}
}

func TestChatCommandHandlerRendersRunSummary(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"id":"owner/repo!7","provider":"github","project_id":"owner/repo","change_id":"7","status":"drafted","title":"Fix validation","web_url":"https://example.test/pr/7","events":[{"type":"run_started"}],"findings":[{"id":"F1"}],"draft_report":"draft","hil_approved":false}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), "/run", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected command to be handled")
	}
	for _, want := range []string{"owner/repo!7  drafted", "provider github", "project  owner/repo", "findings 1", "history  1 events", "draft=5 bytes"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("run command output missing %q:\n%s", want, out.String())
		}
	}
}

func TestChatCommandHandlerRendersStatus(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/ready" {
			t.Fatalf("unexpected status request: %s %s", req.Method, req.URL.String())
		}
		return jsonResponse(http.StatusOK, `{"ready":true,"dependencies":{"headroom":"ok","mempalace":"ok","queue":"ok depth=0 capacity=8"},"queue":{"capacity":8,"available":8}}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "", client)(context.Background(), "/status", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"7review status http://agent", "agent", "headroom", "mempalace", "queue"} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("status command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
}

func TestChatCommandHandlerRendersStatusFailureView(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusServiceUnavailable, `{"ready":false,"dependencies":{"headroom":"down","mempalace":"ok"}}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "", client)(context.Background(), "/status", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !strings.Contains(out.String(), "headroom") || !strings.Contains(out.String(), "down") {
		t.Fatalf("status failure output missing dependency details handled=%t:\n%s", handled, out.String())
	}
}

func TestChatCommandHandlerRendersToolsCatalog(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Path != "/tools" {
			t.Fatalf("unexpected tools request: %s %s", req.Method, req.URL.String())
		}
		return jsonResponse(http.StatusOK, `[{"name":"list_runs","lifecycle_stage":"observe","implemented":true},{"name":"approve_run","lifecycle_stage":"hil","implemented":true,"requires_approval":true}]`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "", client)(context.Background(), "/tools", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tools 2", "list_runs", "observe", "approve_run", "hil", "approval"} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("tools command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
}

func TestChatCommandHandlerRendersProviderStatus(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/tools/execute" {
			t.Fatalf("unexpected provider request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"name":"list_provider_status"`) {
			t.Fatalf("unexpected provider request body: %s", string(body))
		}
		return jsonResponse(http.StatusOK, `{"name":"list_provider_status","result":{"mode":"orchestrator","active_provider":"openrouter","providers":[{"name":"openrouter","configured":true},{"name":"ollama","configured":false,"reason":"missing URL"}],"roles":[{"role":"reasoner","primary":"deepseek@openrouter","fallbacks":["qwen@ollama"],"parallel":true,"max_parallel":2}]}}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "", client)(context.Background(), "/providers", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"providers", "mode     orchestrator", "active   openrouter", "openrouter", "configured", "ollama", "missing URL", "reasoner", "deepseek@openrouter", "fallback=qwen@ollama", "parallel=2"} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("providers command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
}

func TestChatCommandHandlerRendersConfigStatus(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/tools/execute" {
			t.Fatalf("unexpected config request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"name":"get_config_status"`) {
			t.Fatalf("unexpected config request body: %s", string(body))
		}
		return jsonResponse(http.StatusOK, `{"name":"get_config_status","result":{"listen_addr":":8080","corpus_root":"/repo","max_supporting_corpus_sections":5,"memory_dir":"/data","hil_channel":"manual","provider":"openrouter","review_model":"deepseek-chat","small_model":"gpt-4o-mini","orchestrator_config":"./orchestrator.yaml","has_github":true,"has_gitlab":false,"has_openrouter":true,"has_deepseek":true,"headroom_url":"http://headroom:8787","mempalace_url":"http://mempalace:8788","webhook_workers":4,"webhook_queue_size":32}}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "", client)(context.Background(), "/config", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"config", "listen    :8080", "provider  openrouter", "review    deepseek-chat", "support  5", "headroom  http://headroom:8787", "mempalace http://mempalace:8788", "workers  4 queue=32", "github=true gitlab=false", "openrouter=true deepseek=true"} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("config command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
	for _, secret := range []string{"token", "secret", "api_key"} {
		if strings.Contains(strings.ToLower(out.String()), secret) {
			t.Fatalf("config command leaked secret-looking field %q:\n%s", secret, out.String())
		}
	}
}

func TestChatCommandHandlerRendersSkillStatus(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/tools/execute" {
			t.Fatalf("unexpected skills request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"name":"list_skills"`) {
			t.Fatalf("unexpected skills request body: %s", string(body))
		}
		return jsonResponse(http.StatusOK, `{"name":"list_skills","result":[{"name":"traceability-review","path":"agent/skills/traceability-review/SKILL.md","loaded":true},{"name":"framework-rules-review","path":"agent/skills/framework-rules-review/SKILL.md","loaded":false}]}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "", client)(context.Background(), "/skills", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"skills 2", "traceability-review", "loaded", "framework-rules-review", "off", "SKILL.md"} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("skills command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
}

func TestChatCommandHandlerRendersSessions(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/tools/execute" {
			t.Fatalf("unexpected sessions request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"name":"list_runs"`) {
			t.Fatalf("unexpected sessions request body: %s", string(body))
		}
		return jsonResponse(http.StatusOK, `{"name":"list_runs","result":[{"id":"owner/repo!6","provider":"github","project_id":"owner/repo","change_id":"6","title":"Older change","status":"finalized","updated_at":"2026-06-27T11:00:00Z","event_count":2,"hil_approved":true},{"id":"owner/repo!7","provider":"github","project_id":"owner/repo","change_id":"7","title":"Fix validation","status":"drafted","updated_at":"2026-06-27T12:00:00Z","event_count":3}]}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "", client)(context.Background(), "/sessions validation 1", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sessions 1/2", "query=validation", "limit=1", "owner/repo!7", "Fix validation", "history=3", "change owner/repo!7"} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("sessions command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
	if strings.Contains(out.String(), "owner/repo!6") {
		t.Fatalf("sessions filter included non-matching run:\n%s", out.String())
	}
}

func TestChatCommandHandlerPrintsDraftReport(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"id":"owner/repo!7","status":"drafted","draft_report":"draft body"}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), "/draft", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !strings.Contains(out.String(), "agent: draft body") {
		t.Fatalf("unexpected draft command handled=%t out=%s", handled, out.String())
	}
}

func TestChatCommandHandlerRendersMemoryProposal(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/tools/execute" {
			t.Fatalf("unexpected memory request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"name":"preview_memory_proposal"`) || !strings.Contains(string(body), `"run":"owner/repo!7"`) {
			t.Fatalf("unexpected memory request body: %s", string(body))
		}
		return jsonResponse(http.StatusOK, `{"name":"preview_memory_proposal","result":{"run":"owner/repo!7","approved":true,"final_bytes":42,"proposal":{"Conventions":["final report convention"],"Decisions":["human decision"],"Vectors":[{"ID":"v1","Text":"vector text"}]}}}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), "/memory", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"memory owner/repo!7", "approved true", "final_bytes 42", "conventions 1 decisions 1 vectors 1", "final report convention", "human decision", "vector v1"} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("memory command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
}

func TestChatCommandHandlerRendersDiffSummary(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/tools/execute" {
			t.Fatalf("unexpected diff request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"name":"get_diff_summary"`) || !strings.Contains(string(body), `"run":"owner/repo!7"`) {
			t.Fatalf("unexpected diff request body: %s", string(body))
		}
		return jsonResponse(http.StatusOK, `{"name":"get_diff_summary","result":{"run":"owner/repo!7","file_count":2,"total_tokens":123,"additions":10,"deletions":3,"changed_files":[{"path":"api/orders.go","status":"modified","additions":8,"deletions":2,"has_patch":true},{"old_path":"old.go","path":"new.go","status":"renamed","additions":2,"deletions":1,"has_patch":false}]}}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), "/diff", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"diff owner/repo!7", "files 2 tokens 123 +10 -3", "changed", "modified", "api/orders.go", "old.go -> new.go", "no-patch"} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("diff command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
}

func TestChatCommandHandlerRendersSelectedContext(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/tools/execute" {
			t.Fatalf("unexpected context request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		var call tools.ExecuteRequest
		if err := json.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.Name != "get_selected_context" || call.Input["run"] != "owner/repo!7" {
			t.Fatalf("unexpected context tool call: %#v", call)
		}
		return jsonResponse(http.StatusOK, `{"name":"get_selected_context","result":{"run":"owner/repo!7","corpus_sections":[{"path":"docs/SRS.md","title":"REQ-12","kind":"requirement","selection_reason":"seed: identifier REQ-12"},{"path":"docs/openapi.yaml","title":"schemas.Session","kind":"interface","selection_reason":"interface_trace: /sessions -> schemas.Session"}],"evidence_manifest":[{"source":"docs/SRS.md","heading_or_key":"REQ-12","kind":"requirement","authority":"requirement","matched_signals":["REQ-12"],"selection_reason":"seed: identifier REQ-12","score":920,"content_bytes":120},{"source":"docs/openapi.yaml","heading_or_key":"schemas.Session","kind":"interface","authority":"contract","matched_signals":["/sessions","Session"],"selection_reason":"interface_trace: /sessions -> schemas.Session","score":870,"content_bytes":220}],"skill_sections":[{"path":"agent/skills/api-contract-review/SKILL.md","title":"API contract review","kind":"skill","selection_reason":"language Go"}],"warnings":["selected context was compacted"]}}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), "/context", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"context owner/repo!7",
		"corpus 2 evidence 2 skills 1",
		"docs/SRS.md#REQ-12",
		"seed: identifier REQ-12",
		"docs/openapi.yaml#schemas.Session",
		"interface_trace: /sessions -> schemas.Session",
		"signals /sessions, Session",
		"api-contract-review",
		"selected context was compacted",
	} {
		if !handled || !strings.Contains(out.String(), want) {
			t.Fatalf("context command output missing %q handled=%t:\n%s", want, handled, out.String())
		}
	}
}

func TestChatCommandHandlerAcceptsContextAlias(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		var call tools.ExecuteRequest
		if err := json.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.Name != "get_selected_context" {
			t.Fatalf("alias should dispatch to selected context tool, got %q", call.Name)
		}
		return jsonResponse(http.StatusOK, `{"name":"get_selected_context","result":{"run":"owner/repo!7"}}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), "/evidence", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !strings.Contains(out.String(), "context owner/repo!7") {
		t.Fatalf("context alias output unexpected handled=%t:\n%s", handled, out.String())
	}
}

func TestChatCommandHandlerWritesDraftReportToQuotedPath(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"id":"owner/repo!7","status":"drafted","draft_report":"draft body"}`), nil
	})}
	path := filepath.Join(t.TempDir(), "draft report.md")

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), `/draft "`+path+`"`, &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !handled || string(data) != "draft body" || !strings.Contains(out.String(), "draft report written to") {
		t.Fatalf("unexpected draft write handled=%t data=%q out=%s", handled, string(data), out.String())
	}
}

func TestChatCommandHandlerApprovesRunFromReportFile(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "final report.md")
	if err := os.WriteFile(reportPath, []byte("approved final"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotBody string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/approve" || req.URL.Query().Get("run") != "owner/repo!7" {
			t.Fatalf("unexpected approval request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = string(body)
		return jsonResponse(http.StatusAccepted, `{"accepted":true}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), `/approve --report-file "`+reportPath+`"`, &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || gotBody != "approved final" || !strings.Contains(out.String(), "approval queued for owner/repo!7") {
		t.Fatalf("unexpected approval command handled=%t body=%q out=%s", handled, gotBody, out.String())
	}
}

func TestChatCommandHandlerPublishFinalFromReportFile(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "final.md")
	if err := os.WriteFile(reportPath, []byte("approved final retry"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotBody string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/publish/final" || req.URL.Query().Get("run") != "owner/repo!7" {
			t.Fatalf("unexpected publish request: %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		gotBody = string(body)
		return jsonResponse(http.StatusAccepted, `{"accepted":true}`), nil
	})}

	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", client)(context.Background(), "/publish-final --report-file "+reportPath, &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || gotBody != "approved final retry" || !strings.Contains(out.String(), "final publish queued for owner/repo!7") {
		t.Fatalf("unexpected publish command handled=%t body=%q out=%s", handled, gotBody, out.String())
	}
}

func TestChatCommandHandlerRequiresReportFileForHILCommands(t *testing.T) {
	var out strings.Builder
	handled, err := chatCommandHandlerWithClient("http://agent", "owner/repo!7", operatorRequestHTTPClient())(context.Background(), "/approve", &out, ui.ChatContext{}, ui.ChatOptions{Plain: true})
	if !handled || err == nil || !strings.Contains(err.Error(), "--report-file") {
		t.Fatalf("expected report-file error handled=%t err=%v", handled, err)
	}
}
