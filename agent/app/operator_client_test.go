package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/channel"
	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/tools"
	"github.com/Y4NN777/7review/cmd/7review/operator"
)

type lostAcknowledgmentPublisher struct {
	publishes atomic.Int32
	lookups   atomic.Int32
	receipt   tools.AuthorizedPublicationReceipt
}

func (p *lostAcknowledgmentPublisher) PublishAuthorized(_ context.Context, req tools.AuthorizedPublicationRequest) (tools.AuthorizedPublicationReceipt, error) {
	p.publishes.Add(1)
	p.receipt = tools.AuthorizedPublicationReceipt{Provider: req.Destination.Provider, RemoteID: "remote-comment-42", RemoteURL: "https://provider.test/comment/42", PayloadDigest: req.PayloadDigest}
	return tools.AuthorizedPublicationReceipt{}, errors.New("provider acknowledgment lost after remote write")
}

func (p *lostAcknowledgmentPublisher) LookupAuthorized(_ context.Context, req tools.AuthorizedPublicationRequest) (*tools.AuthorizedPublicationReceipt, error) {
	if p.lookups.Add(1) == 1 {
		return nil, nil
	}
	if p.receipt.PayloadDigest != req.PayloadDigest {
		return nil, nil
	}
	receipt := p.receipt
	return &receipt, nil
}

func TestCanonicalOperatorClientAuthenticatesAndReconcilesLostHTTPAcknowledgment(t *testing.T) {
	s, db, dsn := inboxServerDSN(t)
	s.cfg.Operators = appOperators(t)
	s.cfg.APIToken = "legacy-service-secret"
	runtime, err := accounting.OpenRuntime(context.Background(), accounting.RuntimeSettings{DatabaseURL: dsn, BoundaryID: "inbox-test", ProjectID: "team", Operators: s.cfg.Operators}, s.cfg.PolicyRuntimeCeilings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	s.accountingRuntime.Authority = runtime.Authority
	accepted := inboxWebhook(t, s, inboxBody, "operator-client")
	if accepted.Code != 202 {
		t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
	}
	var admission requestReviewResult
	if err = json.Unmarshal(accepted.Body.Bytes(), &admission); err != nil {
		t.Fatal(err)
	}
	s.mux = http.NewServeMux()
	s.routes()
	var drop atomic.Bool
	drop.Store(true)
	var posts, reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/operator/attempt" {
			reads.Add(1)
		}
		if r.URL.Path == "/operator/commands" {
			posts.Add(1)
			if drop.CompareAndSwap(true, false) {
				recorder := httptest.NewRecorder()
				s.mux.ServeHTTP(recorder, r)
				if recorder.Code != 200 {
					t.Errorf("ack lost before successful commit: %d %s", recorder.Code, recorder.Body.String())
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
				return
			}
		}
		s.mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("REVIEW_API_TOKEN", "legacy-service-secret")
	t.Setenv("REVIEW_OPERATOR_TOKEN", "ALICE_TOKEN-secret")
	client := operator.NewClient(server.URL, server.Client())
	ctx := context.Background()
	v, err := client.Attempt(ctx, admission.AttemptID)
	if err != nil || v.ControlVersion != 1 || v.Revision != "head" {
		t.Fatalf("actual status contract: %+v %v", v, err)
	}
	c := review.CancelAttemptCommand{SchemaVersion: 1, CommandID: "client-cancel", AttemptID: v.AttemptID, ExpectedVersion: v.ControlVersion, Revision: v.Revision, Kind: "cancel_attempt", Payload: review.CancelAttemptPayload{Reason: "operator stop"}}
	_, err = client.CancelAttempt(ctx, c)
	var failure *operator.CanonicalRequestError
	if !errors.As(err, &failure) || !failure.MayHaveCommitted || posts.Load() != 1 {
		t.Fatalf("lost ack was retried/acknowledged: %v posts=%d", err, posts.Load())
	}
	r, err := client.CancelAttempt(ctx, c)
	if err != nil || r.ActorID != "alice" || r.ControlVersion != 2 || posts.Load() != 2 {
		t.Fatalf("exact command replay: %+v %v posts=%d", r, err, posts.Load())
	}
	var commands int
	var version uint64
	if err = db.QueryRow(`SELECT (SELECT count(*) FROM budget_operator_commands),control_version FROM budget_jobs WHERE run_id=$1`, admission.RunID).Scan(&commands, &version); err != nil || commands != 1 || version != 2 {
		t.Fatalf("retry duplicated disposition: %d %d %v", commands, version, err)
	}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "BOB_TOKEN-secret")
	if _, err = client.Attempt(ctx, admission.AttemptID); !errors.As(err, &failure) || failure.StatusCode != 403 {
		t.Fatalf("wrong repository credential: %v", err)
	}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "ALICE_TOKEN-secret")
	current, err := s.requestReview(ctx, review.Request{Provider: "github", ProjectID: "org/repo", Repository: "org/repo", ChangeID: "7"})
	if err != nil || current.AttemptID == admission.AttemptID {
		t.Fatalf("fresh explicit intent: %+v %v", current, err)
	}
	c.CommandID = "stale-client-cancel"
	before := reads.Load()
	if _, err = client.CancelAttempt(ctx, c); !errors.As(err, &failure) || failure.StatusCode != 409 || failure.Conflict == nil || failure.Conflict.CurrentAttemptID != current.AttemptID || reads.Load() != before {
		t.Fatalf("stale target was hidden/retargeted: %v", err)
	}
}

func TestCanonicalPublicationAuthorizationHTTPUsesTerminalEvidenceAndMappedReviewer(t *testing.T) {
	s, db, dsn := inboxServerDSN(t)
	directory, err := review.DecodeOperatorDirectory([]byte(`{"schema_version":1,"principals":[{"id":"reviewer","token_env":"REVIEWER_TOKEN","grants":[{"boundary_id":"inbox-test","project_id":"team","provider":"github","host":"github.com","repository_id":"org/repo","roles":["reviewer"]}]},{"id":"participant","token_env":"PARTICIPANT_TOKEN","grants":[{"boundary_id":"inbox-test","project_id":"team","provider":"github","host":"github.com","repository_id":"org/repo","roles":["participant"]}]}]}`), func(name string) string { return strings.ToLower(strings.TrimSuffix(name, "_TOKEN")) + "-secret" })
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Operators = directory
	s.cfg.GitHubAPIURL = "https://api.github.com"
	s.cfg.GitHubToken = "github-token"
	s.cfg.GitHubWebhookSecret = "github-webhook-secret"
	s.cfg.GitHubBotLogin = "seven-review[bot]"
	s.cfg.SCMOperatorTokenEnvs = map[string]string{"github:12345": "PARTICIPANT_TOKEN"}
	accepted := inboxWebhook(t, s, inboxBody, "publication-client")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
	}
	var admission reviewDispatchResult
	if err = json.Unmarshal(accepted.Body.Bytes(), &admission); err != nil {
		t.Fatal(err)
	}
	limits := s.cfg.PolicyRuntimeCeilings.Clone()
	catalog, err := accounting.NewCatalog([]accounting.RouteBound{{Provider: "aggregate-fixture", Model: "pinned", Version: "test-v1", Currency: "USD", EvidenceRef: "test:bounded-publication-provider", ValidFrom: time.Now().Add(-time.Hour), ValidUntil: time.Now().Add(time.Hour), InputHardLimit: 20000, OutputHardLimit: 10000, InputMicroPerMillion: 1, OutputMicroPerMillion: 1, AllOutputBounded: true, MaxActiveMS: 5000, NativeToolsQualified: true}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := accounting.NewAuthority(db, accounting.Config{RequireServiceJobs: true, BoundaryID: "inbox-test", ProjectID: "team", Limits: limits, Catalog: catalog, Operators: directory})
	if err != nil {
		t.Fatal(err)
	}
	s.accountingRuntime.Authority = authority
	s.pipeline.Canonical.Authority = authority
	provider := &aggregateQualifiedProvider{t: t, db: db, attempt: admission.AttemptID, response: `[{"id":"F-1","severity":"high","title":"Authorization bypass","description":"The guard is skipped.","suggestion":"Enforce the guard.","location":{"path":"service.go","line":1},"confidence":0.91}]`}
	build := func() *orchestrator.Orchestrator {
		return orchestrator.NewOrchestrator(orchestrator.DefaultOrchestratorConfig("pinned", "pinned", provider.Name()), map[string]orchestrator.LLMProvider{provider.Name(): provider}, orchestrator.WithOperatorCeilings(&limits), orchestrator.WithBudgetAuthority(authority))
	}
	s.pipeline.Orchestrator = build()
	item, err := authority.ClaimInbox(context.Background())
	if err != nil || item == nil {
		t.Fatalf("claim: %#v %v", item, err)
	}
	ctx := review.WithJobClaim(context.Background(), review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
	err = s.runInboxItem(ctx, item.Request, item.RunID)
	if err != nil && !errors.Is(err, orchestrator.ErrDurableContinuation) {
		t.Fatalf("canonical execution failed: %v", err)
	}
	s.startInboxWorkers()
	terminal := 0
	for until := time.Now().Add(8 * time.Second); time.Now().Before(until); {
		if err = db.QueryRow(`SELECT count(*) FROM budget_jobs WHERE run_id=$1 AND terminal_reduction_id IS NOT NULL`, admission.RunID).Scan(&terminal); err != nil {
			t.Fatal(err)
		}
		if terminal == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.inboxCancel()
	s.inboxWorkers.Wait()
	if terminal != 1 {
		t.Fatal("validated terminal assessment was not completed")
	}
	projection, err := authority.LookupModelJob(context.Background(), admission.RunID)
	if err != nil || !projection.Succeeded || projection.Source.Report.Draft == "" || len(projection.Source.Findings) != 1 {
		t.Fatalf("terminal projection: %#v %v", projection, err)
	}
	var assessmentDigest string
	if err = db.QueryRow(`SELECT m.artifact_digest FROM budget_jobs j JOIN budget_model_reductions m ON m.id=j.terminal_reduction_id WHERE j.run_id=$1`, admission.RunID).Scan(&assessmentDigest); err != nil {
		t.Fatal(err)
	}
	s.mux = http.NewServeMux()
	s.routes()
	server := httptest.NewServer(s.mux)
	defer server.Close()
	client := operator.NewClient(server.URL, server.Client())
	t.Setenv("REVIEW_OPERATOR_TOKEN", "reviewer-secret")
	view, err := client.Attempt(context.Background(), admission.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	beforeCalls := provider.calls.Load()
	t.Setenv("REVIEW_OPERATOR_TOKEN", "participant-secret")
	findingView, err := client.FindingDisposition(context.Background(), admission.AttemptID, projection.Source.Findings[0].ID)
	if err != nil || findingView.Current != nil || findingView.ControlVersion != view.ControlVersion || findingView.Finding.ID != projection.Source.Findings[0].ID {
		t.Fatalf("canonical finding subject: %+v %v", findingView, err)
	}
	disposition := review.FindingDispositionCommand{SchemaVersion: 1, CommandID: "finding-http", AttemptID: admission.AttemptID, ExpectedVersion: findingView.ControlVersion, Revision: findingView.Revision, Kind: "disposition_finding", Payload: review.FindingDispositionPayload{FindingID: findingView.Finding.ID, FindingDigest: findingView.FindingDigest, Decision: review.FindingDisputed, Reason: "the cited branch is unreachable"}}
	dispositionReceipt, err := client.DispositionFinding(context.Background(), disposition)
	if err != nil || dispositionReceipt.ActorID != "participant" || dispositionReceipt.ControlVersion != view.ControlVersion+1 || dispositionReceipt.State != "recorded" {
		t.Fatalf("finding disposition: %+v %v", dispositionReceipt, err)
	}
	staleDisposition := disposition
	staleDisposition.CommandID = "finding-http-stale"
	if _, err = client.DispositionFinding(context.Background(), staleDisposition); err == nil {
		t.Fatal("stale finding disposition accepted")
	} else {
		var failure *operator.CanonicalRequestError
		if !errors.As(err, &failure) || failure.StatusCode != http.StatusConflict {
			t.Fatalf("stale disposition rejection: %v", err)
		}
	}
	if provider.calls.Load() != beforeCalls {
		t.Fatalf("finding disposition dispatched provider I/O: before=%d after=%d", beforeCalls, provider.calls.Load())
	}
	var dispositions int
	if err = db.QueryRow(`SELECT count(*) FROM budget_finding_dispositions WHERE attempt_id=$1`, admission.AttemptID).Scan(&dispositions); err != nil || dispositions != 1 {
		t.Fatalf("finding disposition not durable: %d %v", dispositions, err)
	}
	findingView, err = client.FindingDisposition(context.Background(), admission.AttemptID, disposition.Payload.FindingID)
	if err != nil || findingView.Current == nil || findingView.Current.CommandID != disposition.CommandID || findingView.Current.ActorID != "participant" || findingView.Current.Decision != disposition.Payload.Decision || findingView.Current.Reason != disposition.Payload.Reason || findingView.ControlVersion != dispositionReceipt.ControlVersion {
		t.Fatalf("current finding disposition: %+v %v", findingView, err)
	}
	t.Setenv("PARTICIPANT_TOKEN", "participant-secret")
	s.pipeline.Channels = channel.NewManager([]channel.Config{{Name: "operator_telegram", Provider: "telegram", Enabled: true, AuthorizedSenders: []string{"12345"}, Settings: map[string]string{"operator_token_env_by_sender": `{"12345":"PARTICIPANT_TOKEN"}`}}})
	channelMessage := channel.InboundMessage{Channel: "operator_telegram", ExternalID: "update-canonical-1", SenderID: "12345", Text: "/disposition-finding " + admission.AttemptID + " " + disposition.Payload.FindingID + " disputed finding-channel\nThe cited branch remains unreachable"}
	if _, err = s.pipeline.Channels.VerifyInbound(channelMessage.Channel, "", channelMessage); err != nil {
		t.Fatal(err)
	}
	channelResult, status, err := s.routeChannelMessageContext(context.Background(), channelMessage)
	if err != nil || status != http.StatusOK || !channelResult.Dispositioned || channelResult.ActorID != "participant" || channelResult.CommandID != "finding-channel" || channelResult.ControlVersion != dispositionReceipt.ControlVersion+1 {
		t.Fatalf("canonical channel disposition: %+v status=%d err=%v", channelResult, status, err)
	}
	retryMessage := channelMessage
	retryMessage.ExternalID = "update-canonical-2"
	replayed, status, err := s.routeChannelMessageContext(context.Background(), retryMessage)
	if err != nil || status != http.StatusOK || !replayed.Dispositioned || replayed.ControlVersion != channelResult.ControlVersion || replayed.ActorID != channelResult.ActorID {
		t.Fatalf("durable canonical channel replay: %+v status=%d err=%v", replayed, status, err)
	}
	duplicate, status, err := s.routeChannelMessageContext(context.Background(), retryMessage)
	if err != nil || status != http.StatusAccepted || duplicate.Dispositioned || duplicate.Reason != "duplicate channel delivery" {
		t.Fatalf("duplicate canonical channel delivery: %+v status=%d err=%v", duplicate, status, err)
	}
	if provider.calls.Load() != beforeCalls {
		t.Fatalf("channel disposition dispatched provider I/O: before=%d after=%d", beforeCalls, provider.calls.Load())
	}
	if err = db.QueryRow(`SELECT count(*) FROM budget_finding_dispositions WHERE attempt_id=$1`, admission.AttemptID).Scan(&dispositions); err != nil || dispositions != 2 {
		t.Fatalf("channel disposition history: %d %v", dispositions, err)
	}
	findingView, err = client.FindingDisposition(context.Background(), admission.AttemptID, disposition.Payload.FindingID)
	if err != nil || findingView.Current == nil || findingView.Current.CommandID != "finding-channel" || findingView.Current.ActorID != "participant" || findingView.Current.Reason != "The cited branch remains unreachable" || findingView.ControlVersion != channelResult.ControlVersion {
		t.Fatalf("channel current disposition: %+v %v", findingView, err)
	}
	nativeComment := func(delivery, commentID, actorID, actorName, repository, change, commandID string) *httptest.ResponseRecorder {
		text := "@seven-review[bot] disposition-finding " + admission.AttemptID + " " + disposition.Payload.FindingID + " disputed " + commandID + "\nThe native SCM evidence keeps the dispute open"
		body := fmt.Sprintf(`{"action":"created","repository":{"full_name":%q},"issue":{"number":%s,"pull_request":{"url":"https://api.github.com/pull"}},"sender":{"id":%s,"login":%q},"comment":{"id":%s,"body":%q}}`, repository, change, actorID, actorName, commentID, text)
		req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
		req.Header.Set("X-GitHub-Event", "issue_comment")
		req.Header.Set("X-GitHub-Delivery", delivery)
		req.Header.Set("X-Hub-Signature-256", githubSignature(s.cfg.GitHubWebhookSecret, body))
		rec := httptest.NewRecorder()
		s.mux.ServeHTTP(rec, req)
		return rec
	}
	native := nativeComment("native-delivery-1", "9001", "12345", "participant", "org/repo", "7", "finding-native")
	if native.Code != http.StatusOK {
		t.Fatalf("native SCM disposition: %d %s", native.Code, native.Body.String())
	}
	var nativeResult channel.InboundResult
	if err = json.Unmarshal(native.Body.Bytes(), &nativeResult); err != nil || !nativeResult.Dispositioned || nativeResult.ActorID != "participant" || nativeResult.ControlVersion != channelResult.ControlVersion+1 {
		t.Fatalf("native SCM disposition result: %+v %v", nativeResult, err)
	}
	nativeReplay := nativeComment("native-delivery-2", "9002", "12345", "participant", "org/repo", "7", "finding-native")
	var replayResult channel.InboundResult
	if nativeReplay.Code != http.StatusOK || json.Unmarshal(nativeReplay.Body.Bytes(), &replayResult) != nil || replayResult.ControlVersion != nativeResult.ControlVersion || replayResult.ActorID != nativeResult.ActorID {
		t.Fatalf("durable native SCM replay: %d %s", nativeReplay.Code, nativeReplay.Body.String())
	}
	nativeDuplicate := nativeComment("native-delivery-2", "9002", "12345", "participant", "org/repo", "7", "finding-native")
	if nativeDuplicate.Code != http.StatusAccepted || !strings.Contains(nativeDuplicate.Body.String(), "duplicate SCM comment delivery") {
		t.Fatalf("duplicate native delivery: %d %s", nativeDuplicate.Code, nativeDuplicate.Body.String())
	}
	if wrong := nativeComment("native-wrong-change", "9003", "12345", "participant", "org/repo", "8", "finding-native-wrong"); wrong.Code != http.StatusConflict {
		t.Fatalf("wrong PR context accepted: %d %s", wrong.Code, wrong.Body.String())
	}
	if unknown := nativeComment("native-unknown", "9004", "99999", "participant", "org/repo", "7", "finding-native-unknown"); unknown.Code != http.StatusUnauthorized {
		t.Fatalf("unknown native actor accepted: %d %s", unknown.Code, unknown.Body.String())
	}
	if self := nativeComment("native-self", "9005", "12345", "seven-review[bot]", "org/repo", "7", "finding-native-self"); self.Code != http.StatusAccepted {
		t.Fatalf("bot self-comment not ignored: %d %s", self.Code, self.Body.String())
	}
	if provider.calls.Load() != beforeCalls {
		t.Fatalf("native SCM disposition dispatched provider I/O: before=%d after=%d", beforeCalls, provider.calls.Load())
	}
	if err = db.QueryRow(`SELECT count(*) FROM budget_finding_dispositions WHERE attempt_id=$1`, admission.AttemptID).Scan(&dispositions); err != nil || dispositions != 3 {
		t.Fatalf("native SCM disposition history: %d %v", dispositions, err)
	}
	historicalRetry := channelMessage
	historicalRetry.ExternalID = "update-canonical-3"
	historical, status, err := s.routeChannelMessageContext(context.Background(), historicalRetry)
	if err != nil || status != http.StatusOK || !historical.Dispositioned || historical.CommandID != "finding-channel" || historical.ControlVersion != channelResult.ControlVersion || historical.ActorID != "participant" {
		t.Fatalf("historical exact channel replay after replacement: %+v status=%d err=%v", historical, status, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM budget_finding_dispositions WHERE attempt_id=$1`, admission.AttemptID).Scan(&dispositions); err != nil || dispositions != 3 {
		t.Fatalf("historical replay duplicated disposition: %d %v", dispositions, err)
	}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "reviewer-secret")
	view, err = client.Attempt(context.Background(), admission.AttemptID)
	if err != nil || view.ControlVersion != nativeResult.ControlVersion {
		t.Fatalf("updated attempt view: %+v %v", view, err)
	}
	draftDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(projection.Source.Report.Draft)))
	command := review.AuthorizePublicationCommand{SchemaVersion: 1, CommandID: "publish-http", AttemptID: admission.AttemptID, ExpectedVersion: view.ControlVersion, Revision: view.Revision, Kind: "authorize_publication", Payload: review.AuthorizePublicationPayload{AssessmentDigest: assessmentDigest, ArtifactDigest: draftDigest, Destination: review.PublicationDestination{Provider: "github", Host: "github.com", RepositoryID: "org/repo", ChangeNumber: "7", ArtifactClass: "assessment"}, Reason: "reviewed exact terminal assessment"}}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "participant-secret")
	if _, err = client.AuthorizePublication(context.Background(), command); err == nil {
		t.Fatal("participant authorized publication")
	} else {
		var failure *operator.CanonicalRequestError
		if !errors.As(err, &failure) || failure.StatusCode != http.StatusForbidden {
			t.Fatalf("participant rejection: %v", err)
		}
	}
	t.Setenv("REVIEW_OPERATOR_TOKEN", "reviewer-secret")
	stale := command
	stale.ExpectedVersion--
	stale.CommandID = "publish-http-stale"
	if _, err = client.AuthorizePublication(context.Background(), stale); err == nil {
		t.Fatal("stale publication authorization accepted")
	} else {
		var failure *operator.CanonicalRequestError
		if !errors.As(err, &failure) || failure.StatusCode != http.StatusConflict {
			t.Fatalf("stale rejection: %v", err)
		}
	}
	receipt, err := client.AuthorizePublication(context.Background(), command)
	if err != nil || receipt.ActorID != "reviewer" || receipt.State != "authorized" || receipt.ControlVersion != view.ControlVersion+1 {
		t.Fatalf("publication authorization: %+v %v", receipt, err)
	}
	if provider.calls.Load() != beforeCalls {
		t.Fatalf("authorization dispatched provider I/O: before=%d after=%d", beforeCalls, provider.calls.Load())
	}
	restarted := newInboxServer(t, dsn)
	publicationProvider := &lostAcknowledgmentPublisher{}
	restarted.publicationPublisher = publicationProvider
	restarted.publicationWorkerID = "restart-publication-worker"
	restarted.startInboxWorkers()
	operationState := ""
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		if err = db.QueryRow(`SELECT state FROM budget_publication_operations WHERE command_id=$1`, command.CommandID).Scan(&operationState); err == nil {
			if operationState == "sending" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if operationState != "sending" || publicationProvider.publishes.Load() != 1 {
		t.Fatalf("restart did not perform one fenced publication: state=%q publishes=%d err=%v", operationState, publicationProvider.publishes.Load(), err)
	}
	if _, err = db.Exec(`UPDATE budget_publication_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE command_id=$1`, command.CommandID); err != nil {
		t.Fatal(err)
	}
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		if err = db.QueryRow(`SELECT state FROM budget_publication_operations WHERE command_id=$1`, command.CommandID).Scan(&operationState); err == nil && operationState == "uncertain" && publicationProvider.lookups.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if operationState != "uncertain" || publicationProvider.lookups.Load() != 1 {
		t.Fatalf("missing remote effect was not durably deferred: state=%q lookups=%d err=%v", operationState, publicationProvider.lookups.Load(), err)
	}
	time.Sleep(600 * time.Millisecond)
	if publicationProvider.lookups.Load() != 1 {
		t.Fatalf("deferred uncertain operation was hot-polled: lookups=%d", publicationProvider.lookups.Load())
	}
	if _, err = db.Exec(`UPDATE budget_publication_operations SET reconcile_after=clock_timestamp()-interval '1 second' WHERE command_id=$1`, command.CommandID); err != nil {
		t.Fatal(err)
	}
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		if err = db.QueryRow(`SELECT state FROM budget_publication_operations WHERE command_id=$1`, command.CommandID).Scan(&operationState); err == nil && operationState == "delivered" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	restarted.Close()
	if operationState != "delivered" || publicationProvider.publishes.Load() != 1 || publicationProvider.lookups.Load() == 0 {
		t.Fatalf("lost acknowledgment was not lookup-reconciled without resend: state=%q publishes=%d lookups=%d err=%v", operationState, publicationProvider.publishes.Load(), publicationProvider.lookups.Load(), err)
	}
	if provider.calls.Load() != beforeCalls {
		t.Fatalf("materialization replayed provider work: before=%d after=%d", beforeCalls, provider.calls.Load())
	}
	if runs, listErr := restarted.pipeline.Jobs.List(context.Background()); listErr != nil || len(runs) != 0 {
		t.Fatalf("materialization replayed pipeline: %#v %v", runs, listErr)
	}
}
