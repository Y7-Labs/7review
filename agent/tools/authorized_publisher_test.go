package tools

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Y4NN777/7review/agent/review"
)

func TestGitHubAuthorizedPublicationReturnsAndReconcilesExactRemoteReceipt(t *testing.T) {
	client := NewGitHubClient("http://agent.test", "token")
	req := AuthorizedPublicationRequest{OperationID: "operation-1", PayloadDigest: digestText("exact report"), Destination: review.PublicationDestination{Provider: "github", Host: "agent.test", RepositoryID: "o/r", ChangeNumber: "7", ArtifactClass: "assessment"}, Payload: "exact report"}
	created := false
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/repos/o/r/issues/7/comments" && r.Method == http.MethodGet:
			if created {
				return testJSON(t, []gitHubComment{{ID: 42, Body: authorizedPublicationBody(req), HTMLURL: "https://github.test/comment/42"}}), nil
			}
			return testJSON(t, []gitHubComment{}), nil
		case r.URL.Path == "/repos/o/r/issues/7/comments" && r.Method == http.MethodPost:
			body := readBody(t, r)
			for _, want := range []string{"operation=operation-1", "payload=" + req.PayloadDigest, "exact report"} {
				if !strings.Contains(body, want) {
					t.Fatalf("authorized github body missing %q: %s", want, body)
				}
			}
			created = true
			return testJSON(t, gitHubComment{ID: 42, HTMLURL: "https://github.test/comment/42"}), nil
		}
		return testResponse(http.StatusNotFound, "not found"), nil
	})}
	receipt, err := client.PublishAuthorized(context.Background(), req)
	if err != nil || receipt.RemoteID != "42" || receipt.RemoteURL != "https://github.test/comment/42" || receipt.PayloadDigest != req.PayloadDigest {
		t.Fatalf("github authorized publish: %#v %v", receipt, err)
	}
	found, err := client.LookupAuthorized(context.Background(), req)
	if err != nil || found == nil || *found != receipt {
		t.Fatalf("github authorized reconcile: %#v %v", found, err)
	}
}

func TestGitLabAuthorizedPublicationUpdatesFinalAndRequiresExactPayload(t *testing.T) {
	client := NewGitLabClient("http://agent.test", "token")
	req := AuthorizedPublicationRequest{OperationID: "operation-2", PayloadDigest: digestText("exact report"), Destination: review.PublicationDestination{Provider: "gitlab", Host: "agent.test", RepositoryID: "group/project", ChangeNumber: "9", ArtifactClass: "assessment"}, Payload: "exact report"}
	updated := false
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Path == "/api/v4/projects/group/project/merge_requests/9/notes" && r.Method == http.MethodGet:
			if updated {
				wrong := req
				wrong.Payload = "changed report"
				return testJSON(t, []gitLabNote{{ID: 77, Body: authorizedPublicationBody(wrong), URL: "https://gitlab.test/note/77"}}), nil
			}
			return testJSON(t, []gitLabNote{{ID: 77, Body: reportWithBotMarker("old final", "final"), URL: "https://gitlab.test/note/77"}}), nil
		case r.URL.Path == "/api/v4/projects/group/project/merge_requests/9/notes/77" && r.Method == http.MethodPut:
			updated = true
			return testJSON(t, gitLabNote{ID: 77, URL: "https://gitlab.test/note/77"}), nil
		}
		return testResponse(http.StatusNotFound, "not found"), nil
	})}
	receipt, err := client.PublishAuthorized(context.Background(), req)
	if err != nil || receipt.RemoteID != "77" {
		t.Fatalf("gitlab authorized update: %#v %v", receipt, err)
	}
	found, err := client.LookupAuthorized(context.Background(), req)
	if err != nil || found != nil {
		t.Fatalf("changed remote payload reconciled as exact: %#v %v", found, err)
	}
}

func TestAuthorizedPublicationRejectsEndpointHostDriftBeforeProviderIO(t *testing.T) {
	githubCalls, gitlabCalls := &atomic.Int32{}, &atomic.Int32{}
	github := NewGitHubClient("https://api.github.example", "token")
	github.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		githubCalls.Add(1)
		return testResponse(http.StatusInternalServerError, "unexpected"), nil
	})}
	gitlab := NewGitLabClient("https://gitlab.example", "token")
	gitlab.HTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		gitlabCalls.Add(1)
		return testResponse(http.StatusInternalServerError, "unexpected"), nil
	})}
	for _, tc := range []struct {
		name      string
		publisher AuthorizedPublisher
		provider  string
		calls     *atomic.Int32
	}{
		{name: "github", publisher: github, provider: "github", calls: githubCalls},
		{name: "gitlab", publisher: gitlab, provider: "gitlab", calls: gitlabCalls},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := AuthorizedPublicationRequest{OperationID: "operation-host", PayloadDigest: digestText("report"), Destination: review.PublicationDestination{Provider: tc.provider, Host: "authorized.example", RepositoryID: "o/r", ChangeNumber: "7", ArtifactClass: "assessment"}, Payload: "report"}
			if receipt, err := tc.publisher.PublishAuthorized(context.Background(), req); err == nil || receipt != (AuthorizedPublicationReceipt{}) {
				t.Fatalf("host drift reached publish: %#v %v", receipt, err)
			}
			if receipt, err := tc.publisher.LookupAuthorized(context.Background(), req); err == nil || receipt != nil {
				t.Fatalf("host drift reached lookup: %#v %v", receipt, err)
			}
			if tc.calls.Load() != 0 {
				t.Fatalf("host drift performed %d provider requests", tc.calls.Load())
			}
		})
	}
}
