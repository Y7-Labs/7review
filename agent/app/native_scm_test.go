package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/channel"
	"github.com/Y4NN777/7review/agent/review"
)

func TestGitHubWebhookExtractsSignedPullRequestCommentIdentity(t *testing.T) {
	body := `{
		"action":"created",
		"repository":{"full_name":"org/repo"},
		"issue":{"number":17,"pull_request":{"url":"https://api.github.com/repos/org/repo/pulls/17"}},
		"sender":{"id":123456,"login":"alice"},
		"comment":{"id":9988,"body":"@seven-review[bot] disposition-finding attempt-1 finding-1 disputed command-1\nThe evidence is stale."}
	}`
	var got nativeSCMComment
	handler := gitHubWebhookHandler("secret", func(review.Request) (reviewDispatchResult, error) {
		t.Fatal("review handler must not run for issue comments")
		return reviewDispatchResult{}, nil
	}, func(_ context.Context, comment nativeSCMComment) (channel.InboundResult, int, error) {
		got = comment
		return channel.InboundResult{Dispositioned: true}, http.StatusOK, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "delivery-comment-1")
	req.Header.Set("X-Hub-Signature-256", githubSignature("secret", body))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got.Provider != "github" || got.ActorID != "123456" || got.ActorName != "alice" || got.ProjectID != "org/repo" || got.ChangeID != "17" {
		t.Fatalf("comment identity not normalized: %#v", got)
	}
	if got.CommentID != "9988" || got.DeliveryID != "delivery-comment-1" || !strings.Contains(got.Text, "command-1") {
		t.Fatalf("comment delivery not normalized: %#v", got)
	}
}

func TestGitHubWebhookIgnoresIssueCommentsOutsidePullRequests(t *testing.T) {
	body := `{"action":"created","repository":{"full_name":"org/repo"},"issue":{"number":17},"sender":{"id":123,"login":"alice"},"comment":{"id":99,"body":"command"}}`
	called := false
	handler := gitHubWebhookHandler("secret", func(review.Request) (reviewDispatchResult, error) {
		return reviewDispatchResult{}, nil
	}, func(context.Context, nativeSCMComment) (channel.InboundResult, int, error) {
		called = true
		return channel.InboundResult{}, http.StatusOK, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/webhook/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", githubSignature("secret", body))
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusAccepted || called {
		t.Fatalf("non-PR comment must be ignored: status=%d called=%v", rec.Code, called)
	}
}

func TestGitLabWebhookExtractsAuthenticatedMergeRequestNoteIdentity(t *testing.T) {
	body := `{
		"object_kind":"note","event_type":"note","user_id":456,
		"user":{"id":456,"username":"bob"},"project":{"id":42},
		"merge_request":{"iid":9},
		"object_attributes":{"id":8877,"noteable_type":"MergeRequest","note":"@seven-review-bot disposition-finding attempt-2 finding-2 rejected command-2\nFalse positive."}
	}`
	var got nativeSCMComment
	handler := gitLabWebhookHandler("secret", func(review.Request) (reviewDispatchResult, error) {
		t.Fatal("review handler must not run for notes")
		return reviewDispatchResult{}, nil
	}, func(_ context.Context, comment nativeSCMComment) (channel.InboundResult, int, error) {
		got = comment
		return channel.InboundResult{Dispositioned: true}, http.StatusOK, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/webhook/gitlab", strings.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "secret")
	req.Header.Set("X-Gitlab-Event-UUID", "delivery-note-1")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got.Provider != "gitlab" || got.ActorID != "456" || got.ActorName != "bob" || got.ProjectID != "42" || got.ChangeID != "9" {
		t.Fatalf("note identity not normalized: %#v", got)
	}
	if got.CommentID != "8877" || got.DeliveryID != "delivery-note-1" || !strings.Contains(got.Text, "command-2") {
		t.Fatalf("note delivery not normalized: %#v", got)
	}
}

func TestGitLabWebhookRejectsConflictingActorIDs(t *testing.T) {
	body := `{"object_kind":"note","user_id":456,"user":{"id":789,"username":"bob"},"project":{"id":42},"merge_request":{"iid":9},"object_attributes":{"id":88,"noteable_type":"MergeRequest","note":"command"}}`
	called := false
	handler := gitLabWebhookHandler("secret", func(review.Request) (reviewDispatchResult, error) {
		return reviewDispatchResult{}, nil
	}, func(context.Context, nativeSCMComment) (channel.InboundResult, int, error) {
		called = true
		return channel.InboundResult{}, http.StatusOK, nil
	})
	req := httptest.NewRequest(http.MethodPost, "/webhook/gitlab", strings.NewReader(body))
	req.Header.Set("X-Gitlab-Token", "secret")
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusBadRequest || called {
		t.Fatalf("conflicting actor IDs must fail closed: status=%d called=%v", rec.Code, called)
	}
}
