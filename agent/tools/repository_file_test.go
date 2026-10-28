package tools

import (
	"context"
	"encoding/base64"
	"errors"
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
			return testJSON(t, map[string]any{"sha": rootTree, "truncated": false, "tree": []any{map[string]any{"path": ".7review", "mode": "040000", "type": "tree", "sha": subTree}}}), nil
		case "/repos/o/r/git/trees/" + subTree:
			return testJSON(t, map[string]any{"sha": subTree, "truncated": false, "tree": []any{map[string]any{"path": "review.yaml", "mode": "100644", "type": "blob", "sha": blob, "size": len(body)}}}), nil
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
	body := []byte("schema_version: 2")
	blob := testGitBlobID(body)
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if missing {
			return testResponse(http.StatusNotFound, "missing"), nil
		}
		if strings.HasSuffix(r.URL.Path, "/commits/"+repositoryTestRevision) {
			return testJSON(t, map[string]any{"id": repositoryTestRevision}), nil
		}
		if strings.HasSuffix(r.URL.Path, "/repository/tree") {
			if r.URL.Query().Get("ref") != repositoryTestRevision {
				t.Fatalf("revision not bound: %s", r.URL)
			}
			if r.URL.Query().Get("path") == "" {
				return testJSON(t, []any{map[string]any{"name": ".7review", "path": ".7review", "mode": "040000", "type": "tree", "id": strings.Repeat("b", 40)}}), nil
			}
			if r.URL.Query().Get("path") != ".7review" {
				t.Fatalf("unexpected tree path: %s", r.URL)
			}
			return testJSON(t, []any{map[string]any{"name": "review.yaml", "path": ".7review/review.yaml", "mode": "100644", "type": "blob", "id": blob}}), nil
		}
		if r.URL.Path != "/api/v4/projects/42/repository/blobs/"+blob+"/raw" {
			t.Fatalf("unexpected raw blob request: %s", r.URL)
		}
		return testResponse(200, string(body)), nil
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
