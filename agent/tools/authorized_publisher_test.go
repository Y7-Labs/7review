package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/review"
)

func TestGitHubAuthorizedPublicationReturnsAndReconcilesExactRemoteReceipt(t *testing.T) {
	client := NewGitHubClient("http://agent.test", "token")
	req := AuthorizedPublicationRequest{OperationID: "operation-1", PayloadDigest: digestText("exact report"), Destination: review.PublicationDestination{Provider: "github", Host: "github.com", RepositoryID: "o/r", ChangeNumber: "7", ArtifactClass: "assessment"}, Payload: "exact report"}
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
	req := AuthorizedPublicationRequest{OperationID: "operation-2", PayloadDigest: digestText("exact report"), Destination: review.PublicationDestination{Provider: "gitlab", Host: "gitlab.test", RepositoryID: "group/project", ChangeNumber: "9", ArtifactClass: "assessment"}, Payload: "exact report"}
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
