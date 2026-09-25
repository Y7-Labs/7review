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
	client := NewGitHubClient("http://agent.test", "token")
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/o/r/contents/.7review/review.yaml" || r.URL.Query().Get("ref") != "base-sha" {
			t.Fatalf("unexpected trusted file request: %s", r.URL.String())
		}
		if missing {
			return testResponse(http.StatusNotFound, `{"message":"Not Found"}`), nil
		}
		return testJSON(t, map[string]string{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("schema_version: 2"))}), nil
	})}
	data, err := client.ReadRepositoryFile(context.Background(), "o/r", "base-sha", ".7review/review.yaml")
	if err != nil || string(data) != "schema_version: 2" {
		t.Fatalf("unexpected file result %q: %v", data, err)
	}
	missing = true
	if _, err := client.ReadRepositoryFile(context.Background(), "o/r", "base-sha", ".7review/review.yaml"); !errors.Is(err, ErrRepositoryFileNotFound) {
		t.Fatalf("expected normalized not found, got %v", err)
	}
}

func TestGitLabReadRepositoryFileBindsRevisionAndNormalizesNotFound(t *testing.T) {
	client := NewGitLabClient("http://agent.test", "token")
	missing := false
	client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("ref") != "base-sha" {
			t.Fatalf("revision not bound: %s", r.URL.String())
		}
		if missing {
			return testResponse(http.StatusNotFound, "missing"), nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("schema_version: 2"))}, nil
	})}
	data, err := client.ReadRepositoryFile(context.Background(), "42", "base-sha", ".7review/review.yaml")
	if err != nil || string(data) != "schema_version: 2" {
		t.Fatalf("unexpected file result %q: %v", data, err)
	}
	missing = true
	if _, err := client.ReadRepositoryFile(context.Background(), "42", "base-sha", ".7review/review.yaml"); !errors.Is(err, ErrRepositoryFileNotFound) {
		t.Fatalf("expected normalized not found, got %v", err)
	}
}
