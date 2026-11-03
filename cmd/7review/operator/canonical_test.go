package operator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

const canonicalViewFixture = `{"attempt_id":"attempt-1","run_id":"run-1","revision":"head","inbox_state":"cancelled","job_phase":"finished","current":true,"active":false,"deadline":"2026-01-01T00:00:00Z","control_version":3,"released_unused_actions":1,"retained_model_reservations":0}`

func canonicalCommand() review.CancelAttemptCommand {
	return review.CancelAttemptCommand{SchemaVersion: 1, CommandID: "cancel-1", AttemptID: "attempt-1", ExpectedVersion: 1, Revision: "head", Kind: "cancel_attempt", Payload: review.CancelAttemptPayload{Reason: "operator stop"}}
}
func canonicalReceipt(c review.CancelAttemptCommand) string {
	raw, _ := json.Marshal(review.CancelAttemptReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: "sha256:" + strings.Repeat("a", 64), AttemptID: c.AttemptID, ActorID: "alice", Kind: c.Kind, Revision: c.Revision, ControlVersion: c.ExpectedVersion + 1, State: "cancelled", ReservationDisposition: "retained", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	return string(raw)
}

func canonicalPublicationCommand() review.AuthorizePublicationCommand {
	return review.AuthorizePublicationCommand{
		SchemaVersion: 1, CommandID: "publish-1", AttemptID: "attempt-1", ExpectedVersion: 2, Revision: "head", Kind: "authorize_publication",
		Payload: review.AuthorizePublicationPayload{
			AssessmentDigest: "sha256:" + strings.Repeat("a", 64),
			ArtifactDigest:   "sha256:" + strings.Repeat("b", 64),
			Destination:      review.PublicationDestination{Provider: "github", Host: "github.com", RepositoryID: "org/repo", ChangeNumber: "7", ArtifactClass: "assessment"},
			Reason:           "reviewed exact assessment",
		},
	}
}

func canonicalPublicationReceipt(c review.AuthorizePublicationCommand) string {
	raw, _ := json.Marshal(review.PublicationAuthorizationReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: "sha256:" + strings.Repeat("c", 64), AttemptID: c.AttemptID, ActorID: "alice", Kind: c.Kind, Revision: c.Revision, ControlVersion: c.ExpectedVersion + 1, AssessmentDigest: c.Payload.AssessmentDigest, ArtifactDigest: c.Payload.ArtifactDigest, Destination: c.Payload.Destination, State: "authorized", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	return string(raw)
}

func canonicalSettlementCommand() review.UsageSettlementCommand {
	return review.UsageSettlementCommand{SchemaVersion: 1, CommandID: "settle-1", ActionID: "action-1", Kind: "settle_model_usage", Payload: review.UsageSettlementPayload{InputTokens: 10, OutputTokens: 2, MoneyMicro: 1, ActiveMS: 20, EvidenceID: "invoice-42", Reason: "reconciled provider invoice"}}
}

func canonicalSettlementReceipt(c review.UsageSettlementCommand) string {
	digest, _ := review.CanonicalUsageSettlementCommandDigest(c)
	raw, _ := json.Marshal(review.UsageSettlementReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: digest, ActionID: c.ActionID, ActorID: "alice", Kind: c.Kind, Actual: c.Payload.Actual(), EvidenceRef: "operator:alice:" + c.Payload.EvidenceID, State: "settled"})
	return string(raw)
}

func canonicalFindingDispositionCommand() review.FindingDispositionCommand {
	return review.FindingDispositionCommand{SchemaVersion: 1, CommandID: "finding-1", AttemptID: "attempt-1", ExpectedVersion: 2, Revision: "head", Kind: "disposition_finding", Payload: review.FindingDispositionPayload{FindingID: "F-1", FindingDigest: "sha256:" + strings.Repeat("d", 64), Decision: review.FindingDisputed, Reason: "the branch is unreachable"}}
}

func canonicalFindingDispositionReceipt(c review.FindingDispositionCommand) string {
	raw, _ := json.Marshal(review.FindingDispositionReceipt{SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: "sha256:" + strings.Repeat("e", 64), AttemptID: c.AttemptID, ActorID: "alice", Kind: c.Kind, Revision: c.Revision, ControlVersion: c.ExpectedVersion + 1, FindingID: c.Payload.FindingID, FindingDigest: c.Payload.FindingDigest, Decision: c.Payload.Decision, State: "recorded", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	return string(raw)
}

func canonicalFindingDispositionView(t *testing.T) review.FindingDispositionView {
	t.Helper()
	finding := review.Finding{ID: "F-1", Severity: review.SeverityHigh, Title: "Authorization bypass", Description: "The guard is skipped.", Suggestion: "Enforce the guard.", Location: review.Location{Path: "service.go", Line: 7}, Confidence: 0.91}
	digest, err := review.CanonicalFindingDigest(finding)
	if err != nil {
		t.Fatal(err)
	}
	return review.FindingDispositionView{SchemaVersion: 1, AttemptID: "attempt-1", Revision: "head", ControlVersion: 2, Finding: finding, FindingDigest: digest}
}

func TestCanonicalClientUsesOnlyMappedOperatorCredentialAndExactJSONCommand(t *testing.T) {
	t.Setenv("REVIEW_API_TOKEN", "legacy-secret")
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	c := canonicalCommand()
	calls := 0
	client := NewClient("https://agent/prefix", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer operator-secret" || r.Header.Get("X-7review-Token") != "" {
			t.Fatalf("mixed service/operator credentials: %#v", r.Header)
		}
		if r.Method == http.MethodGet {
			if r.URL.Path != "/prefix/operator/attempt" || r.URL.Query().Get("attempt_id") != "attempt-1" || len(r.URL.Query()) != 1 {
				t.Fatalf("unscoped read: %s", r.URL.String())
			}
			return jsonResponse(200, canonicalViewFixture), nil
		}
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/operator/commands" || r.Header.Get("Content-Type") != "application/json" || r.URL.RawQuery != "" {
			t.Fatalf("wrong command transport: %s %s", r.Method, r.URL.String())
		}
		raw, _ := io.ReadAll(r.Body)
		got, err := review.DecodeCancelAttemptCommand(raw)
		if err != nil || got != c || strings.Contains(string(raw), "actor") {
			t.Fatalf("command changed: %+v %v", got, err)
		}
		return jsonResponse(200, canonicalReceipt(c)), nil
	})})
	v, err := client.Attempt(context.Background(), "attempt-1")
	if err != nil || v.ReleasedUnusedActions != 1 {
		t.Fatalf("attempt: %+v %v", v, err)
	}
	r, err := client.CancelAttempt(context.Background(), c)
	if err != nil || r.CommandID != c.CommandID || calls != 2 {
		t.Fatalf("cancel: %+v %d %v", r, calls, err)
	}
}

func TestCanonicalClientSendsExactPublicationAuthorizationAndRequiresExactReceipt(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	c := canonicalPublicationCommand()
	calls := 0
	client := NewClient("https://agent/prefix", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/operator/commands" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer operator-secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("wrong publication transport: %s %s %#v", r.Method, r.URL.String(), r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		got, err := review.DecodeAuthorizePublicationCommand(raw)
		if err != nil || got != c || strings.Contains(string(raw), "actor") {
			t.Fatalf("publication command changed: %+v %v", got, err)
		}
		return jsonResponse(http.StatusOK, canonicalPublicationReceipt(c)), nil
	})})
	r, err := client.AuthorizePublication(context.Background(), c)
	if err != nil || r.CommandID != c.CommandID || r.ControlVersion != 3 || calls != 1 {
		t.Fatalf("publication authorization: %+v calls=%d err=%v", r, calls, err)
	}
}

func TestCanonicalClientSendsExactFindingDispositionAndRequiresExactReceipt(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	c := canonicalFindingDispositionCommand()
	calls := 0
	client := NewClient("https://agent/prefix", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/operator/commands" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer operator-secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("wrong disposition transport: %s %s %#v", r.Method, r.URL.String(), r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		got, err := review.DecodeFindingDispositionCommand(raw)
		if err != nil || got != c || strings.Contains(string(raw), "actor") {
			t.Fatalf("finding disposition changed: %+v %v", got, err)
		}
		return jsonResponse(http.StatusOK, canonicalFindingDispositionReceipt(c)), nil
	})})
	r, err := client.DispositionFinding(context.Background(), c)
	if err != nil || r.CommandID != c.CommandID || r.ControlVersion != 3 || calls != 1 {
		t.Fatalf("finding disposition: %+v calls=%d err=%v", r, calls, err)
	}
	for _, body := range []string{"{}", canonicalFindingDispositionReceipt(c) + `{}`, strings.Replace(canonicalFindingDispositionReceipt(c), `"finding_digest":"`+c.Payload.FindingDigest+`"`, `"finding_digest":"sha256:`+strings.Repeat("f", 64)+`"`, 1)} {
		calls = 0
		client = NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, body), nil
		})})
		_, err = client.DispositionFinding(context.Background(), c)
		var failure *CanonicalRequestError
		if !errors.As(err, &failure) || !failure.MayHaveCommitted || failure.CommandID != c.CommandID || calls != 1 {
			t.Fatalf("unproven disposition acknowledged/retried: failure=%+v calls=%d err=%v", failure, calls, err)
		}
	}
}

func TestCanonicalClientSendsExactUsageSettlementAndRequiresExactReceipt(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	c := canonicalSettlementCommand()
	calls := 0
	client := NewClient("https://agent/prefix", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/prefix/operator/commands" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer operator-secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("wrong settlement transport: %s %s %#v", r.Method, r.URL.String(), r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		got, err := review.DecodeUsageSettlementCommand(raw)
		if err != nil || got != c || strings.Contains(string(raw), `"actor_id":`) || strings.Contains(string(raw), `"provider":`) || strings.Contains(string(raw), `"request_digest":`) {
			t.Fatalf("settlement command changed or widened: %+v %v", got, err)
		}
		return jsonResponse(http.StatusOK, canonicalSettlementReceipt(c)), nil
	})})
	receipt, err := client.SettleUsage(context.Background(), c)
	if err != nil || receipt.CommandID != c.CommandID || receipt.Actual != c.Payload.Actual() || calls != 1 {
		t.Fatalf("usage settlement: %+v calls=%d err=%v", receipt, calls, err)
	}
	bad := strings.Replace(canonicalSettlementReceipt(c), `"output_tokens":2`, `"output_tokens":3`, 1)
	client = NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, bad), nil
	})})
	_, err = client.SettleUsage(context.Background(), c)
	var failure *CanonicalRequestError
	if !errors.As(err, &failure) || !failure.MayHaveCommitted || failure.CommandID != c.CommandID {
		t.Fatalf("unproven settlement acknowledged: %+v %v", failure, err)
	}
}

func TestCanonicalClientReadsExactFindingDispositionSubject(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	want := canonicalFindingDispositionView(t)
	raw, _ := json.Marshal(want)
	calls := 0
	client := NewClient("https://agent/prefix", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/prefix/operator/finding" || r.URL.Query().Get("attempt_id") != want.AttemptID || r.URL.Query().Get("finding_id") != want.Finding.ID || len(r.URL.Query()) != 2 || r.Body != nil || r.Header.Get("Authorization") != "Bearer operator-secret" {
			t.Fatalf("wrong finding read transport: %s %s %#v", r.Method, r.URL.String(), r.Header)
		}
		return jsonResponse(http.StatusOK, string(raw)), nil
	})})
	got, err := client.FindingDisposition(context.Background(), want.AttemptID, want.Finding.ID)
	if err != nil || got.FindingDigest != want.FindingDigest || calls != 1 {
		t.Fatalf("finding view: %+v calls=%d err=%v", got, calls, err)
	}
	for _, invalid := range []string{"", " bad", "bad?id"} {
		if _, err = client.FindingDisposition(context.Background(), want.AttemptID, invalid); err == nil || calls != 1 {
			t.Fatalf("invalid finding ID reached transport: %q %v", invalid, err)
		}
	}
	changed := strings.Replace(string(raw), `"Title":"Authorization bypass"`, `"Title":"changed"`, 1)
	client = NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, changed), nil
	})})
	if _, err = client.FindingDisposition(context.Background(), want.AttemptID, want.Finding.ID); err == nil {
		t.Fatal("client accepted finding bytes that do not match the authority digest")
	}
}

func TestCanonicalClientReadsExactBoundedActionProjection(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	want := review.OperatorActionsView{SchemaVersion: 1, AttemptID: "attempt-1", Actions: []review.OperatorActionView{{ActionID: "action-1", State: "sending", Provider: "qualified", Model: "pinned-model", Role: "reasoner", Step: "review", Currency: "USD", RouteVersion: "contract-v1", Upper: review.ModelUsageActual{InputTokens: 100, OutputTokens: 20, MoneyMicro: 4, ActiveMS: 50}, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}}}
	raw, _ := json.Marshal(want)
	calls := 0
	client := NewClient("https://agent/prefix", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/prefix/operator/actions" || r.URL.Query().Get("attempt_id") != want.AttemptID || len(r.URL.Query()) != 1 || r.Body != nil || r.Header.Get("Authorization") != "Bearer operator-secret" {
			t.Fatalf("wrong actions transport: %s %s %#v", r.Method, r.URL.String(), r.Header)
		}
		return jsonResponse(http.StatusOK, string(raw)), nil
	})})
	got, err := client.Actions(context.Background(), want.AttemptID)
	if err != nil || len(got.Actions) != 1 || got.Actions[0].ActionID != "action-1" || calls != 1 {
		t.Fatalf("actions view: %+v calls=%d err=%v", got, calls, err)
	}
	for _, invalid := range []string{"", " bad", "bad?id"} {
		if _, err = client.Actions(context.Background(), invalid); err == nil || calls != 1 {
			t.Fatalf("invalid attempt ID reached actions transport: %q %v", invalid, err)
		}
	}
	changed := strings.Replace(string(raw), `"action_id":"action-1"`, `"action_id":"action-1","request_digest":"secret"`, 1)
	client = NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, changed), nil
	})})
	if _, err = client.Actions(context.Background(), want.AttemptID); err == nil {
		t.Fatal("client accepted widened action projection")
	}
}

func TestCanonicalPublicationClientNeverRetriesRetargetsOrAcknowledgesUnprovenReceipt(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	c := canonicalPublicationCommand()
	for _, body := range []string{"{}", canonicalPublicationReceipt(c) + `{}`, strings.Replace(canonicalPublicationReceipt(c), `"artifact_digest":"`+c.Payload.ArtifactDigest+`"`, `"artifact_digest":"sha256:`+strings.Repeat("d", 64)+`"`, 1)} {
		calls := 0
		client := NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return jsonResponse(http.StatusOK, body), nil
		})})
		_, err := client.AuthorizePublication(context.Background(), c)
		var failure *CanonicalRequestError
		if !errors.As(err, &failure) || !failure.MayHaveCommitted || failure.CommandID != c.CommandID || calls != 1 || strings.Contains(err.Error(), "cancellation outcome") {
			t.Fatalf("unproven publication response acknowledged/retried: failure=%+v calls=%d err=%v", failure, calls, err)
		}
	}
	calls := 0
	client := NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost {
			t.Fatal("publication conflict fetched a newer attempt")
		}
		return jsonResponse(http.StatusConflict, `{"current_attempt_id":"attempt-2","control_version":7,"reason":"stale revision","current_attempt_url":"/operator/attempt?attempt_id=attempt-2"}`), nil
	})})
	_, err := client.AuthorizePublication(context.Background(), c)
	var failure *CanonicalRequestError
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusConflict || failure.Conflict == nil || failure.Conflict.CurrentAttemptID != "attempt-2" || failure.MayHaveCommitted || failure.CommandID != c.CommandID || calls != 1 {
		t.Fatalf("publication conflict: %+v calls=%d err=%v", failure, calls, err)
	}
}

func TestCanonicalClientRejectsMissingCredentialAndUnsafeServerBeforeIO(t *testing.T) {
	t.Setenv("REVIEW_API_TOKEN", "legacy-secret")
	t.Setenv("REVIEW_OPERATOR_TOKEN", "")
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(200, canonicalViewFixture), nil
	})}
	if _, err := NewClient("https://agent", httpClient).Attempt(context.Background(), "attempt-1"); err == nil {
		t.Fatal("shared service token established operator identity")
	}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	for _, server := range []string{"http://remote.example", "https://user:secret@agent", "https://agent?token=secret", "https://agent/#fragment", "relative", "file:///tmp/agent"} {
		if _, err := NewClient(server, httpClient).CancelAttempt(context.Background(), canonicalCommand()); err == nil {
			t.Fatalf("unsafe server accepted: %s", server)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid authority caused IO: %d", calls)
	}
}

func TestCanonicalClientDoesNotFollowRedirectsOrMutateSharedHTTPClient(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	var redirected atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Add(1)
		w.Write([]byte(canonicalReceipt(canonicalCommand())))
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	shared := source.Client()
	if _, err := NewClient(source.URL, shared).CancelAttempt(context.Background(), canonicalCommand()); err == nil {
		t.Fatal("redirect became success")
	}
	if redirected.Load() != 0 || shared.CheckRedirect != nil {
		t.Fatal("redirect forwarded credentials or changed shared client")
	}
}

func TestCanonicalClientConflictIsReturnedWithoutRefetchOrRetarget(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	calls := 0
	client := NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost {
			t.Fatal("conflict fetched a newer attempt")
		}
		return jsonResponse(409, `{"current_attempt_id":"attempt-2","control_version":7,"reason":"stale revision","current_attempt_url":"/operator/attempt?attempt_id=attempt-2"}`), nil
	})})
	_, err := client.CancelAttempt(context.Background(), canonicalCommand())
	var failure *CanonicalRequestError
	if !errors.As(err, &failure) || failure.StatusCode != 409 || failure.Conflict == nil || failure.Conflict.CurrentAttemptID != "attempt-2" || failure.MayHaveCommitted || calls != 1 {
		t.Fatalf("conflict: %+v calls=%d err=%v", failure, calls, err)
	}
}

func TestCanonicalClientNeverAcknowledgesMalformedWrongOrPartialReceipts(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	c := canonicalCommand()
	for _, body := range []string{"{}", canonicalReceipt(c) + `{}`, strings.Replace(canonicalReceipt(c), `"attempt_id":"attempt-1"`, `"attempt_id":"other"`, 1), canonicalReceipt(c) + strings.Repeat(" ", 20000)} {
		client := NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return jsonResponse(200, body), nil })})
		_, err := client.CancelAttempt(context.Background(), c)
		var failure *CanonicalRequestError
		if !errors.As(err, &failure) || !failure.MayHaveCommitted {
			t.Fatalf("unproven response acknowledged: %v", err)
		}
	}
	client := NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("untrusted failure contains operator-secret")
	})})
	_, err := client.CancelAttempt(context.Background(), c)
	if err == nil || strings.Contains(err.Error(), "operator-secret") {
		t.Fatalf("transport secret leaked: %v", err)
	}
}

func TestCanonicalClientClassifiesPlainHTTPRejectionsWithoutLeakingResponseText(t *testing.T) {
	t.Setenv("REVIEW_OPERATOR_TOKEN", "operator-secret")
	for _, status := range []int{400, 401, 403, 404, 409, 503} {
		client := NewClient("https://agent", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			r := jsonResponse(status, "untrusted response operator-secret")
			r.Header.Set("Content-Type", "text/plain")
			return r, nil
		})})
		_, err := client.CancelAttempt(context.Background(), canonicalCommand())
		var failure *CanonicalRequestError
		if !errors.As(err, &failure) || failure.StatusCode != status || failure.MayHaveCommitted != (status == 503) || strings.Contains(err.Error(), "operator-secret") {
			t.Fatalf("HTTP %d rejection: %+v %v", status, failure, err)
		}
	}
}
