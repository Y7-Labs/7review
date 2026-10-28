package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubReadRepositoryFileBindsRevision(t *testing.T) {
	missing := false
	rootTree, subTree := strings.Repeat("b", 40), strings.Repeat("c", 40)
	body := []byte("schema_version: 2")
	blob := testGitBlobID(body)
	client := NewGitHubClient("http://agent.test", "token")
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if missing {
			return testResponse(http.StatusNotFound, `{"message":"Not Found"}`), nil
		}
		switch r.URL.Path {
		case "/repos/o/r/git/commits/" + repositoryTestRevision:
			return testJSON(t, map[string]any{"sha": repositoryTestRevision, "tree": map[string]any{"sha": rootTree}}), nil
		case "/repos/o/r/git/trees/" + rootTree:
			return testJSON(t, map[string]any{"sha": rootTree, "tree": []any{map[string]any{"path": ".7review", "mode": "040000", "type": "tree", "sha": subTree}}}), nil
		case "/repos/o/r/git/trees/" + subTree:
			return testJSON(t, map[string]any{"sha": subTree, "tree": []any{map[string]any{"path": "review.yaml", "mode": "100644", "type": "blob", "sha": blob, "size": len(body)}}}), nil
		case "/repos/o/r/git/blobs/" + blob:
			return testJSON(t, map[string]any{"sha": blob, "size": len(body), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(body)}), nil
		default:
			t.Fatalf("unexpected trusted object request: %s", r.URL)
			return nil, nil
		}
	})}
	data, err := client.ReadRepositoryFile(context.Background(), "o/r", repositoryTestRevision, ".7review/review.yaml")
	if err != nil || string(data) != "schema_version: 2" {
		t.Fatalf("unexpected file result %q: %v", data, err)
	}
	missing = true
	if _, err := client.ReadRepositoryFile(context.Background(), "o/r", repositoryTestRevision, ".7review/review.yaml"); !errors.Is(err, ErrRepositoryFileNotFound) {
		t.Fatalf("expected normalized not found, got %v", err)
	}
}

func TestGitLabReadRepositoryFileBindsRevisionAndNormalizesNotFound(t *testing.T) {
	client := NewGitLabClient("http://agent.test", "token")
	missing := false
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("ref") != repositoryTestRevision {
			t.Fatalf("revision not bound: %s", r.URL.String())
		}
		if missing {
			return testResponse(http.StatusNotFound, "missing"), nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("schema_version: 2"))}, nil
	})}
	data, err := client.ReadRepositoryFile(context.Background(), "42", repositoryTestRevision, ".7review/review.yaml")
	if err != nil || string(data) != "schema_version: 2" {
		t.Fatalf("unexpected file result %q: %v", data, err)
	}
	missing = true
	if _, err := client.ReadRepositoryFile(context.Background(), "42", repositoryTestRevision, ".7review/review.yaml"); !errors.Is(err, ErrRepositoryFileNotFound) {
		t.Fatalf("expected normalized not found, got %v", err)
	}
}
