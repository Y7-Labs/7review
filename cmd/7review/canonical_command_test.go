package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/ui"
	"github.com/Y4NN777/7review/cmd/7review/operator"
	tea "github.com/charmbracelet/bubbletea"
)

const cliAttemptFixture = `{"attempt_id":"attempt-1","run_id":"run-1","revision":"head","inbox_state":"cancelled","job_phase":"finished","current":true,"active":false,"deadline":"2026-01-01T00:00:00Z","control_version":3,"released_unused_actions":1,"retained_model_reservations":0}`

func cancelArgs() []string {
	return []string{"attempt-1", "--command-id", "cancel-1", "--expected-version", "1", "--revision", "head", "--reason", "operator stop", "--server", "https://agent"}
}
func cliCancelResponse(c review.CancelAttemptCommand) string {
	raw, _ := json.Marshal(review.CancelAttemptReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: "sha256:" + strings.Repeat("a", 64), AttemptID: c.AttemptID, ActorID: "alice", Kind: c.Kind, Revision: c.Revision, ControlVersion: c.ExpectedVersion + 1, State: "cancelled", ReservationDisposition: "retained", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	return string(raw)
}

func publicationArgs() []string {
	return []string{"attempt-1", "--command-id", "publish-1", "--expected-version", "2", "--revision", "head", "--assessment-digest", "sha256:" + strings.Repeat("a", 64), "--artifact-digest", "sha256:" + strings.Repeat("b", 64), "--provider", "github", "--host", "github.com", "--repository-id", "org/repo", "--change-number", "7", "--artifact-class", "assessment", "--reason", "reviewed exact assessment", "--server", "https://agent"}
}

func cliPublicationResponse(c review.AuthorizePublicationCommand) string {
	raw, _ := json.Marshal(review.PublicationAuthorizationReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: "sha256:" + strings.Repeat("c", 64), AttemptID: c.AttemptID, ActorID: "alice", Kind: c.Kind, Revision: c.Revision, ControlVersion: c.ExpectedVersion + 1, AssessmentDigest: c.Payload.AssessmentDigest, ArtifactDigest: c.Payload.ArtifactDigest, Destination: c.Payload.Destination, State: "authorized", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	return string(raw)
}

func dispositionArgs() []string {
	return []string{"attempt-1", "F-1", "--command-id", "finding-1", "--decision", "disputed", "--reason", "the cited branch is unreachable", "--server", "https://agent"}
}

func cliFindingView(t *testing.T) review.FindingDispositionView {
	t.Helper()
	finding := review.Finding{ID: "F-1", Severity: review.SeverityHigh, Title: "Authorization bypass", Description: "The guard is skipped.", Suggestion: "Enforce the guard.", Location: review.Location{Path: "service.go", Line: 7}, Confidence: 0.91}
	digest, err := review.CanonicalFindingDigest(finding)
	if err != nil {
		t.Fatal(err)
	}
	return review.FindingDispositionView{SchemaVersion: 1, AttemptID: "attempt-1", Revision: "head", ControlVersion: 2, Finding: finding, FindingDigest: digest}
}

func cliFindingReceipt(c review.FindingDispositionCommand) string {
	raw, _ := json.Marshal(review.FindingDispositionReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: "sha256:" + strings.Repeat("e", 64), AttemptID: c.AttemptID, ActorID: "alice", Kind: c.Kind, Revision: c.Revision, ControlVersion: c.ExpectedVersion + 1, FindingID: c.Payload.FindingID, FindingDigest: c.Payload.FindingDigest, Decision: c.Payload.Decision, State: "recorded", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	return string(raw)
}

func TestCanonicalCLIShowsScopedStatusAndSubmitsOneExactCancellation(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	t.Setenv("REVIEW_API_TOKEN", "legacy-secret")
	var requests []string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer operator-secret" || r.Header.Get("X-7review-Token") != "" {
			t.Fatal("CLI used wrong authority")
		}
		if r.Method == http.MethodGet {
			if r.URL.Path != "/operator/attempt" || r.URL.Query().Get("attempt_id") != "attempt-1" {
				t.Fatal("CLI resolved a run alias")
			}
			return jsonResponse(200, cliAttemptFixture), nil
		}
		if r.URL.Path != "/operator/commands" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatal("CLI used legacy mutation endpoint")
		}
		raw, _ := io.ReadAll(r.Body)
		requests = append(requests, string(raw))
		c, err := review.DecodeCancelAttemptCommand(raw)
		if err != nil || c.AttemptID != "attempt-1" || c.Revision != "head" || c.ExpectedVersion != 1 || c.CommandID != "cancel-1" || c.Payload.Reason != "operator stop" {
			t.Fatalf("CLI changed command: %+v %v", c, err)
		}
		return jsonResponse(200, cliCancelResponse(c)), nil
	})}
	var out strings.Builder
	if err := runOperatorAttempt([]string{"attempt-1", "--server", "https://agent", "--json"}, &out, client); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"released_unused_actions":1`) || !strings.Contains(out.String(), `"control_version":3`) {
		t.Fatalf("wrong status: %s", out.String())
	}
	for n := 0; n < 2; n++ {
		out.Reset()
		if err := runCancelAttempt(cancelArgs(), &out, client); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "cancelled attempt-1") || !strings.Contains(out.String(), "cancel-1") || !strings.Contains(out.String(), "retained") {
			t.Fatalf("unproven output: %s", out.String())
		}
	}
	if len(requests) != 2 || requests[0] != requests[1] {
		t.Fatal("manual retry changed command identity/payload")
	}
}

func TestCanonicalCLISubmitsOneExactPublicationAuthorizationWithoutLookup(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/operator/commands" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer operator-secret" {
			t.Fatalf("publication CLI used wrong transport: %s %s", r.Method, r.URL.String())
		}
		raw, _ := io.ReadAll(r.Body)
		c, err := review.DecodeAuthorizePublicationCommand(raw)
		if err != nil || c.CommandID != "publish-1" || c.AttemptID != "attempt-1" || c.ExpectedVersion != 2 || c.Revision != "head" || c.Payload.Destination != (review.PublicationDestination{Provider: "github", Host: "github.com", RepositoryID: "org/repo", ChangeNumber: "7", ArtifactClass: "assessment"}) || c.Payload.Reason != "reviewed exact assessment" {
			t.Fatalf("publication CLI changed command: %+v %v", c, err)
		}
		return jsonResponse(http.StatusOK, cliPublicationResponse(c)), nil
	})}
	var out strings.Builder
	if err := runAuthorizePublication(publicationArgs(), &out, client); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out.String(), "authorized publication for attempt-1") || !strings.Contains(out.String(), "publish-1") || !strings.Contains(out.String(), "github.com/org/repo change 7") {
		t.Fatalf("unproven publication output/calls: %d %s", calls, out.String())
	}
}

func TestCanonicalCLIFetchesAndDispositionsOneExactFinding(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	view := cliFindingView(t)
	viewRaw, _ := json.Marshal(view)
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.Method != http.MethodGet || r.URL.Path != "/operator/finding" || r.URL.Query().Get("attempt_id") != view.AttemptID || r.URL.Query().Get("finding_id") != view.Finding.ID {
				t.Fatalf("CLI did not read exact finding: %s %s", r.Method, r.URL.String())
			}
			return jsonResponse(http.StatusOK, string(viewRaw)), nil
		}
		if r.Method != http.MethodPost || r.URL.Path != "/operator/commands" || r.URL.RawQuery != "" {
			t.Fatalf("CLI used wrong disposition transport: %s %s", r.Method, r.URL.String())
		}
		raw, _ := io.ReadAll(r.Body)
		command, err := review.DecodeFindingDispositionCommand(raw)
		if err != nil || command.CommandID != "finding-1" || command.AttemptID != view.AttemptID || command.ExpectedVersion != view.ControlVersion || command.Revision != view.Revision || command.Payload.FindingID != view.Finding.ID || command.Payload.FindingDigest != view.FindingDigest || command.Payload.Decision != review.FindingDisputed || command.Payload.Reason != "the cited branch is unreachable" {
			t.Fatalf("CLI changed authoritative finding command: %+v %v", command, err)
		}
		return jsonResponse(http.StatusOK, cliFindingReceipt(command)), nil
	})}
	var out strings.Builder
	if err := runDispositionFinding(dispositionArgs(), &out, client); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !strings.Contains(out.String(), "disputed finding F-1 for attempt-1") || !strings.Contains(out.String(), "finding-1") || !strings.Contains(out.String(), "control version 3") {
		t.Fatalf("unproven finding output/calls: %d %s", calls, out.String())
	}
}

func TestCanonicalFindingCLIRejectsImplicitBindingsBeforeIO(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusOK, "{}"), nil
	})}
	complete := dispositionArgs()
	for _, args := range [][]string{
		nil,
		{"attempt-1", "F-1"},
		append(append([]string(nil), complete...), "--expected-version", "2"),
		append(append([]string(nil), complete...), "--finding-digest", "sha256:"+strings.Repeat("a", 64)),
		append(append([]string(nil), complete...), "--decision", "suppressed"),
		append(append([]string(nil), complete...), "extra"),
	} {
		var out strings.Builder
		if err := runDispositionFinding(args, &out, client); err == nil || out.Len() != 0 {
			t.Fatalf("unsafe disposition args accepted: %v", args)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid disposition reached authority: %d", calls)
	}
}

func TestCanonicalPublicationCLIRejectsIncompleteDuplicateOrImplicitTargetsBeforeIO(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusOK, "{}"), nil
	})}
	complete := publicationArgs()
	cases := [][]string{
		nil,
		{"attempt-1"},
		append(append([]string(nil), complete...), "--expected-version=3"),
		append(append([]string(nil), complete...), "--repository-id=other/repo"),
		append(append([]string(nil), complete...), "extra"),
		append(append([]string(nil), complete...), "--unknown"),
		{"attempt-1", "--command-id", "publish-1", "--expected-version", "2", "--revision", "head", "--assessment-digest", "sha256:" + strings.Repeat("a", 64), "--artifact-digest", "sha256:" + strings.Repeat("b", 64), "--provider", "github", "--repository-id", "org/repo", "--change-number", "7", "--artifact-class", "assessment", "--reason", "reviewed"},
	}
	for _, args := range cases {
		var out strings.Builder
		if err := runAuthorizePublication(args, &out, client); err == nil || out.Len() != 0 {
			t.Fatalf("ambiguous publication target accepted: %#v output=%s", args, out.String())
		}
	}
	if calls != 0 {
		t.Fatalf("invalid publication command reached network: %d", calls)
	}
}

func TestCanonicalCLIRejectsMissingDuplicateAmbiguousOrUnknownArgumentsBeforeIO(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return jsonResponse(200, "{}"), nil })}
	cases := [][]string{nil, {"attempt-1"}, append(cancelArgs(), "--expected-version=2"), append(cancelArgs(), "extra"), append(cancelArgs(), "--unknown"), append(cancelArgs(), "--reason=operator-secret"), {"owner/repo!7", "--command-id", "cancel-1", "--expected-version", "1", "--revision", "head", "--reason", "stop"}, {"attempt-1", "--command-id", "cancel-1", "--expected-version", "01", "--revision", "head", "--reason", "stop"}, {"attempt-1", "--command-id", "cancel-1", "--expected-version", "1", "--revision", "head", "--reason", "--json"}}
	for _, args := range cases {
		var out strings.Builder
		err := runCancelAttempt(args, &out, client)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "operator-secret") {
			t.Fatalf("ambiguous args accepted/leaked: %v %s", err, out.String())
		}
	}
	if calls != 0 {
		t.Fatal("invalid command reached the network")
	}
}

func TestCanonicalCLIConflictDoesNotReadLatestOrRetarget(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost {
			t.Fatal("CLI fetched latest during mutation")
		}
		return jsonResponse(409, `{"current_attempt_id":"attempt-2","control_version":7,"reason":"stale","current_attempt_url":"/operator/attempt?attempt_id=attempt-2"}`), nil
	})}
	var out strings.Builder
	err := runCancelAttempt(cancelArgs(), &out, client)
	var failure *operator.CanonicalRequestError
	if !errors.As(err, &failure) || failure.Conflict == nil || failure.Conflict.CurrentAttemptID != "attempt-2" || calls != 1 || out.Len() != 0 {
		t.Fatalf("conflict was hidden/retargeted: %v %s", err, out.String())
	}
}

func TestCanonicalTerminalCommandsRequireExplicitAttemptAndHonorCancellation(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if c, err := review.DecodeCancelAttemptCommand(raw); err == nil {
			if c.AttemptID != "attempt-1" || c.Payload.Reason != "operator stop" {
				t.Fatalf("terminal rebound cancellation: %+v", c)
			}
			return jsonResponse(200, cliCancelResponse(c)), nil
		}
		c, err := review.DecodeAuthorizePublicationCommand(raw)
		if err != nil || c.AttemptID != "attempt-1" || c.Payload.Destination.RepositoryID != "org/repo" {
			t.Fatalf("terminal rebound publication: %+v %v", c, err)
		}
		return jsonResponse(200, cliPublicationResponse(c)), nil
	})}
	handler := chatCommandHandlerWithClient("https://agent", "owner/repo!7", client)
	var out strings.Builder
	for _, text := range []string{"/attempt", "/cancel-attempt", "/authorize-publication", `/cancel-attempt attempt-1 --command-id cancel-1 --expected-version 1 --revision head --reason stop --server https://other`, `/authorize-publication attempt-1 --server https://other`} {
		if handled, err := handler(context.Background(), text, &out, ui.ChatContext{}, ui.ChatOptions{Plain: true}); !handled || err == nil {
			t.Fatalf("missing/foreign target accepted: %s %v", text, err)
		}
	}
	if calls != 0 {
		t.Fatal("terminal used selected run as mutation authority")
	}
	text := `/cancel-attempt attempt-1 --command-id cancel-1 --expected-version 1 --revision head --reason "operator stop"`
	if handled, err := handler(context.Background(), text, &out, ui.ChatContext{}, ui.ChatOptions{Plain: true}); !handled || err != nil || !strings.Contains(out.String(), "cancelled attempt-1") {
		t.Fatalf("terminal cancellation: %v %s", err, out.String())
	}
	publication := `/authorize-publication attempt-1 --command-id publish-1 --expected-version 2 --revision head --assessment-digest sha256:` + strings.Repeat("a", 64) + ` --artifact-digest sha256:` + strings.Repeat("b", 64) + ` --provider github --host github.com --repository-id org/repo --change-number 7 --artifact-class assessment --reason "reviewed exact assessment"`
	if handled, err := handler(context.Background(), publication, &out, ui.ChatContext{}, ui.ChatOptions{Plain: true}); !handled || err != nil || !strings.Contains(out.String(), "authorized publication for attempt-1") {
		t.Fatalf("terminal publication authorization: %v %s", err, out.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handler(ctx, text, &out, ui.ChatContext{}, ui.ChatOptions{Plain: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("terminal ignored cancelled context: %v", err)
	}
	if calls != 2 {
		t.Fatal("cancelled terminal command reached network")
	}
}

func TestCanonicalConsoleInterruptionKeepsOutcomeUnconfirmedUntilRequestReturns(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	started := make(chan struct{}, 1)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	m := newConsoleTUIModel(client, tuiCommandOptions{serverURL: "https://agent"})
	m.input = `/authorize-publication attempt-1 --command-id publish-1 --expected-version 2 --revision head --assessment-digest sha256:` + strings.Repeat("a", 64) + ` --artifact-digest sha256:` + strings.Repeat("b", 64) + ` --provider github --host github.com --repository-id org/repo --change-number 7 --artifact-class assessment --reason "reviewed exact assessment"`
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(consoleTUIModel)
	if m.cancel == nil || cmd == nil {
		t.Fatal("canonical console request has no cancellable context")
	}
	defer m.cancel()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("console request never started")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = updated.(consoleTUIModel)
	if !m.commandRunning || strings.Contains(m.message, "stream cancelled") {
		t.Fatal("console claimed interruption completed the mutation")
	}
	var result tea.Msg
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("console did not cancel HTTP context")
	}
	commandResult, ok := result.(consoleCommandMsg)
	var failure *operator.CanonicalRequestError
	if !ok || !errors.As(commandResult.err, &failure) || !failure.MayHaveCommitted {
		t.Fatalf("unknown commit outcome hidden: %+v", result)
	}
	updated, _ = m.Update(commandResult)
	m = updated.(consoleTUIModel)
	if m.commandRunning {
		t.Fatal("console remained busy after request finished")
	}
}

func TestCanonicalConsoleIgnoresLateModelStreamEventsDuringOperatorRequest(t *testing.T) {
	m := newConsoleTUIModel(nil, tuiCommandOptions{})
	m.commandRunning = true
	m.cancel = func() {}
	m.stream = nil
	m.transcript = appendTranscript(m.transcript, "you", "/cancel-attempt attempt-1")
	updated, cmd := m.Update(consoleStreamMsg{delta: "late old response", done: true})
	m = updated.(consoleTUIModel)
	if !m.commandRunning || m.cancel == nil || cmd != nil || len(m.transcript) != 1 {
		t.Fatal("old model stream overwrote current operator request")
	}
}

func TestCanonicalConsoleReceiptCannotBeOverwrittenByAnOlderStream(t *testing.T) {
	m := newConsoleTUIModel(nil, tuiCommandOptions{})
	m.transcript = appendTranscript(m.transcript, "agent", "cancelled attempt-1")
	old := make(chan consoleStreamMsg, 1)
	old <- consoleStreamMsg{delta: "old model text", done: true}
	msg := waitConsoleStream(old)()
	updated, cmd := m.Update(msg)
	m = updated.(consoleTUIModel)
	if cmd != nil || len(m.transcript) != 1 || m.transcript[0].Text != "cancelled attempt-1" {
		t.Fatal("stale stream overwrote the canonical receipt")
	}
}

func TestCanonicalCLIExecutableChild(t *testing.T) {
	server := os.Getenv("SEVENREVIEW_CANONICAL_CLI_TEST_SERVER")
	if server == "" {
		t.Skip("subprocess helper")
	}
	command, args := "cancel-attempt", cancelArgs()
	if os.Getenv("SEVENREVIEW_CANONICAL_CLI_TEST_MODE") == "publication" {
		command, args = "authorize-publication", publicationArgs()
	}
	os.Args = append([]string{"7review", command}, args...)
	os.Args = append(os.Args[:len(os.Args)-2], "--server", server, "--json")
	main()
	os.Exit(0)
}

func TestCanonicalCLIExecutableDispatchesPublicationAuthorizationAndEmitsValidatedJSON(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/operator/commands" || r.Header.Get("Authorization") != "Bearer operator-secret" {
			t.Error("executable used wrong publication route/credential")
		}
		raw, _ := io.ReadAll(r.Body)
		c, err := review.DecodeAuthorizePublicationCommand(raw)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(cliPublicationResponse(c)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCanonicalCLIExecutableChild$")
	child.Env = append(os.Environ(), "SEVENREVIEW_CANONICAL_CLI_TEST_SERVER="+server.URL, "SEVENREVIEW_CANONICAL_CLI_TEST_MODE=publication")
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("executable failed: %v %s", err, out)
	}
	var receipt review.PublicationAuthorizationReceipt
	if err = json.Unmarshal(out, &receipt); err != nil || receipt.CommandID != "publish-1" || receipt.AttemptID != "attempt-1" || receipt.Destination.RepositoryID != "org/repo" || requests.Load() != 1 {
		t.Fatalf("executable publication acknowledgment: %+v requests=%d err=%v out=%s", receipt, requests.Load(), err, out)
	}
}

func TestCanonicalCLIExecutableDispatchesCancellationAndEmitsValidatedJSON(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/operator/commands" || r.Header.Get("Authorization") != "Bearer operator-secret" {
			t.Error("executable used wrong route/credential")
		}
		raw, _ := io.ReadAll(r.Body)
		c, err := review.DecodeCancelAttemptCommand(raw)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(cliCancelResponse(c)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCanonicalCLIExecutableChild$")
	child.Env = append(os.Environ(), "SEVENREVIEW_CANONICAL_CLI_TEST_SERVER="+server.URL)
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("executable failed: %v %s", err, out)
	}
	var receipt review.CancelAttemptReceipt
	if err = json.Unmarshal(out, &receipt); err != nil || receipt.CommandID != "cancel-1" || receipt.AttemptID != "attempt-1" || requests.Load() != 1 {
		t.Fatalf("executable acknowledgment: %+v requests=%d err=%v out=%s", receipt, requests.Load(), err, out)
	}
}
