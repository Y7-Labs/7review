package tools

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

var repositoryTestRevision = strings.Repeat("a", 40)

func repositoryReader(provider, base string, client *http.Client) RepositoryFileReader {
	if provider == "github" {
		c := NewGitHubClient(base, "test-token")
		c.HTTPClient = client
		return c
	}
	c := NewGitLabClient(base, "test-token")
	c.HTTPClient = client
	return c
}

func TestRepositoryTransportBoundsAndCancellation(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 100)))}, nil
	})}
	r, err := newRepositoryHTTP("github", "http://agent.test", "Authorization", "Bearer test", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.get(context.Background(), "/fixed", 10); err == nil {
		t.Fatal("oversized response accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := calls
	if _, _, err := r.get(ctx, "/fixed", 100); err != context.Canceled || calls != before {
		t.Fatalf("cancelled read crossed network: calls=%d err=%v", calls, err)
	}
	for i := r.requests; i < maxRepositoryReadRequests; i++ {
		if _, _, err := r.get(context.Background(), "/fixed", 100); err != nil {
			t.Fatal(err)
		}
	}
	before = calls
	if _, _, err := r.get(context.Background(), "/fixed", 100); err == nil || calls != before {
		t.Fatal("request bound failed")
	}
}

func TestRepositoryReadersRejectAmbiguousAPIOrigins(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		for _, origin := range []string{"http://user:pass@agent.test", "http://agent.test?other=repository", "http://agent.test#fragment", "file:///tmp/repo"} {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return testResponse(404, "missing"), nil })}
			if _, err := repositoryReader(provider, origin, client).ReadRepositoryFile(context.Background(), "o/r", repositoryTestRevision, ".7review/review.yaml"); err == nil || calls != 0 {
				t.Fatalf("ambiguous origin reached transport: provider=%s origin=%s calls=%d err=%v", provider, origin, calls, err)
			}
		}
	}
}

func TestRepositoryReadersRejectUnsafeInputsBeforeNetwork(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			for _, tc := range []struct{ repo, revision, path string }{
				{"o/r", repositoryTestRevision, "../secret"}, {"o/r", repositoryTestRevision, ".."}, {"o/r", repositoryTestRevision, "/secret"},
				{"o/r", repositoryTestRevision, "a/../secret"}, {"o/r", repositoryTestRevision, "a\\secret"}, {"o/r", repositoryTestRevision, "a%2fsecret"},
				{"o/r", repositoryTestRevision, "a\nsecret"}, {"o/../r", repositoryTestRevision, ".7review/review.yaml"},
				{"o/r?other=repo", repositoryTestRevision, ".7review/review.yaml"}, {"o/r", "main", ".7review/review.yaml"},
			} {
				calls := 0
				client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return testResponse(404, "missing"), nil })}
				_, err := repositoryReader(provider, "http://agent.test", client).ReadRepositoryFile(context.Background(), tc.repo, tc.revision, tc.path)
				if err == nil || calls != 0 {
					t.Fatalf("unsafe input reached provider: %#v calls=%d err=%v", tc, calls, err)
				}
			}
		})
	}
}

func TestRepositoryReadersNeverFollowCredentialBearingRedirects(t *testing.T) {
	for _, provider := range []string{"github", "gitlab"} {
		t.Run(provider, func(t *testing.T) {
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirected.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if provider == "github" {
					w.Write([]byte(`{"type":"file","encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte("policy")) + `"}`))
				} else {
					w.Write([]byte("policy"))
				}
			}))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL+"/foreign/repository", http.StatusTemporaryRedirect)
			}))
			defer origin.Close()
			client := &http.Client{}
			_, err := repositoryReader(provider, origin.URL, client).ReadRepositoryFile(context.Background(), "o/r", repositoryTestRevision, ".7review/review.yaml")
			if err == nil || redirected.Load() != 0 {
				t.Fatalf("redirect crossed repository/credential boundary: requests=%d err=%v", redirected.Load(), err)
			}
			if client.CheckRedirect != nil {
				t.Fatal("reader mutated caller-owned shared HTTP client")
			}
		})
	}
}
