package tools

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestGitLabRepositoryFileRequiresBoundRegularBlob(t *testing.T) {
	for _, tc := range []struct {
		name, mode, kind                                                          string
		paginated, duplicate, badPage, tampered, wrongPath, linkOnly, wrongCommit bool
		allow                                                                     bool
	}{
		{name: "regular", mode: "100644", kind: "blob", allow: true},
		{name: "executable regular", mode: "100755", kind: "blob", allow: true},
		{name: "symlink", mode: "120000", kind: "blob"},
		{name: "submodule", mode: "160000", kind: "commit"},
		{name: "directory", mode: "040000", kind: "tree"},
		{name: "later page", mode: "100644", kind: "blob", paginated: true, allow: true},
		{name: "duplicate across pages", mode: "100644", kind: "blob", duplicate: true},
		{name: "duplicate after link-only pagination", mode: "100644", kind: "blob", duplicate: true, linkOnly: true},
		{name: "invalid pagination", mode: "100644", kind: "blob", badPage: true},
		{name: "wrong path", mode: "100644", kind: "blob", wrongPath: true},
		{name: "tampered bytes", mode: "100644", kind: "blob", tampered: true},
		{name: "mismatched commit receipt", mode: "100644", kind: "blob", wrongCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte{0xff, 0, 'p', 'o', 'l', 'i', 'c', 'y'}
			blob := testGitBlobID(body)
			trees, blobs := 0, 0
			c := NewGitLabClient("http://agent.test/gitlab", "token")
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "agent.test" {
					t.Fatalf("followed foreign pagination URL: %s", r.URL)
				}
				switch r.URL.Path {
				case "/gitlab/api/v4/projects/42/repository/commits/" + repositoryTestRevision:
					id := repositoryTestRevision
					if tc.wrongCommit {
						id = strings.Repeat("f", 40)
					}
					return testJSON(t, map[string]any{"id": id}), nil
				case "/gitlab/api/v4/projects/42/repository/files/policy.md/raw":
					return testResponse(200, string(body)), nil
				case "/gitlab/api/v4/projects/42/repository/tree":
					trees++
					if r.URL.Query().Get("ref") != repositoryTestRevision || r.URL.Query().Get("path") != "" || r.URL.Query().Get("per_page") != "100" {
						t.Fatalf("tree escaped frozen query: %s", r.URL)
					}
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					if page < 1 || page > 2 {
						t.Fatalf("unexpected page %s", r.URL)
					}
					path := "policy.md"
					if tc.wrongPath {
						path = "other-repo/policy.md"
					}
					entry := map[string]any{"name": "policy.md", "path": path, "type": tc.kind, "mode": tc.mode, "id": blob}
					entries := []any{entry}
					if page == 1 && tc.paginated {
						entries = []any{map[string]any{"name": "other.md", "path": "other.md", "type": "blob", "mode": "100644", "id": blob}}
					}
					resp := testJSON(t, entries)
					resp.Header.Set("X-Next-Page", "")
					if page == 1 && (tc.paginated || tc.duplicate) {
						resp.Header.Set("X-Next-Page", "2")
						resp.Header.Set("Link", `<http://attacker.test/foreign/tree>; rel="next"`)
						if tc.linkOnly {
							resp.Header.Del("X-Next-Page")
						}
					}
					if tc.badPage {
						resp.Header.Set("X-Next-Page", "1")
					}
					return resp, nil
				case "/gitlab/api/v4/projects/42/repository/blobs/" + blob + "/raw":
					blobs++
					if tc.tampered {
						return testResponse(200, "changed blob"), nil
					}
					return testResponse(200, string(body)), nil
				default:
					t.Fatalf("unexpected GitLab repository request: %s", r.URL)
					return nil, nil
				}
			})}
			data, err := c.ReadRepositoryFile(context.Background(), "42", repositoryTestRevision, "policy.md")
			if tc.allow {
				expected := 1
				if tc.paginated {
					expected = 2
				}
				if err != nil || string(data) != string(body) || blobs != 1 || trees != expected {
					t.Fatalf("immutable raw blob not read: data=%q trees=%d blobs=%d err=%v", data, trees, blobs, err)
				}
			} else if err == nil || data != nil {
				t.Fatalf("unconfined GitLab file accepted: data=%q err=%v", data, err)
			}
			if !tc.allow && !tc.tampered && blobs != 0 {
				t.Fatal("invalid tree authority reached raw blob endpoint")
			}
		})
	}
}

func TestGitLabRepositoryAncestorAndNamespaceConfinement(t *testing.T) {
	for _, mode := range []string{"040000", "120000"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte("policy")
			blob := testGitBlobID(body)
			rawReads := 0
			c := NewGitLabClient("http://agent.test", "token")
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if !strings.HasPrefix(r.URL.EscapedPath(), "/api/v4/projects/group%2Frepo/repository/") {
					t.Fatalf("namespace escaped project endpoint: %s", r.URL)
				}
				if strings.HasSuffix(r.URL.Path, "/commits/"+repositoryTestRevision) {
					return testJSON(t, map[string]any{"id": repositoryTestRevision}), nil
				}
				if strings.HasSuffix(r.URL.Path, "/tree") {
					if r.URL.Query().Get("ref") != repositoryTestRevision {
						t.Fatal("tree changed revision")
					}
					if r.URL.Query().Get("path") == "" {
						kind := "tree"
						if mode == "120000" {
							kind = "blob"
						}
						return testJSON(t, []any{map[string]any{"name": ".7review", "path": ".7review", "mode": mode, "type": kind, "id": strings.Repeat("b", 40)}}), nil
					}
					if r.URL.Query().Get("path") != ".7review" {
						t.Fatal("changed directory")
					}
					return testJSON(t, []any{map[string]any{"name": "review.yaml", "path": ".7review/review.yaml", "mode": "100644", "type": "blob", "id": blob}}), nil
				}
				rawReads++
				return testResponse(200, string(body)), nil
			})}
			data, err := c.ReadRepositoryFile(context.Background(), "group/repo", repositoryTestRevision, ".7review/review.yaml")
			if mode == "040000" {
				if err != nil || string(data) != "policy" || rawReads != 1 {
					t.Fatalf("normal namespace path failed: data=%q err=%v", data, err)
				}
			} else if err == nil || rawReads != 0 {
				t.Fatalf("symlink ancestor followed: reads=%d err=%v", rawReads, err)
			}
		})
	}
}

func TestGitLabRepositoryPaginationIsBoundedWithoutHeaders(t *testing.T) {
	c := NewGitLabClient("http://agent.test", "token")
	calls := 0
	entries := make([]any, 100)
	for i := range entries {
		entries[i] = map[string]any{"name": "other" + strconv.Itoa(i), "path": "other" + strconv.Itoa(i), "mode": "100644", "type": "blob", "id": strings.Repeat("b", 40)}
	}
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/commits/"+repositoryTestRevision) {
			return testJSON(t, map[string]any{"id": repositoryTestRevision}), nil
		}
		return testJSON(t, entries), nil
	})}
	if _, err := c.ReadRepositoryFile(context.Background(), "42", repositoryTestRevision, "policy.md"); err == nil || !strings.Contains(err.Error(), "bound") || calls != maxRepositoryReadRequests {
		t.Fatalf("unbounded or silently truncated pagination: requests=%d err=%v", calls, err)
	}
}
