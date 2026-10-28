package tools

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func testGitBlobID(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d%c", len(data), byte(0))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func TestGitHubRepositoryFileRequiresBoundRegularBlob(t *testing.T) {
	for _, tc := range []struct {
		name, mode, kind                                                  string
		truncated, wrongCommit, wrongTree, wrongBlob, tampered, duplicate bool
		allow                                                             bool
	}{
		{name: "regular", mode: "100644", kind: "blob", allow: true},
		{name: "executable regular", mode: "100755", kind: "blob", allow: true},
		{name: "symlink disguised by contents API", mode: "120000", kind: "blob"},
		{name: "submodule", mode: "160000", kind: "commit"},
		{name: "directory", mode: "040000", kind: "tree"},
		{name: "truncated tree", mode: "100644", kind: "blob", truncated: true},
		{name: "wrong commit", mode: "100644", kind: "blob", wrongCommit: true},
		{name: "wrong tree", mode: "100644", kind: "blob", wrongTree: true},
		{name: "wrong blob receipt", mode: "100644", kind: "blob", wrongBlob: true},
		{name: "tampered content", mode: "100644", kind: "blob", tampered: true},
		{name: "duplicate path", mode: "100644", kind: "blob", duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte("trusted policy\n")
			blob := testGitBlobID(body)
			tree := strings.Repeat("b", 40)
			blobReads := 0
			treeReads := 0
			client := NewGitHubClient("http://agent.test/api/v3", "token")
			client.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "agent.test" {
					t.Fatalf("followed provider-supplied URL: %s", r.URL)
				}
				switch r.URL.Path {
				case "/api/v3/repos/o/r/contents/policy.md":
					return testJSON(t, map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString(body)}), nil
				case "/api/v3/repos/o/r/git/commits/" + repositoryTestRevision:
					sha := repositoryTestRevision
					if tc.wrongCommit {
						sha = strings.Repeat("f", 40)
					}
					return testJSON(t, map[string]any{"sha": sha, "tree": map[string]any{"sha": tree}}), nil
				case "/api/v3/repos/o/r/git/trees/" + tree:
					treeReads++
					sha := tree
					if tc.wrongTree {
						sha = strings.Repeat("f", 40)
					}
					entry := map[string]any{"path": "policy.md", "mode": tc.mode, "type": tc.kind, "sha": blob, "size": len(body), "url": "http://attacker.test/blob"}
					entries := []any{entry}
					if tc.duplicate {
						entries = append(entries, entry)
					}
					return testJSON(t, map[string]any{"sha": sha, "tree": entries, "truncated": tc.truncated}), nil
				case "/api/v3/repos/o/r/git/blobs/" + blob:
					blobReads++
					sha := blob
					if tc.wrongBlob {
						sha = strings.Repeat("f", 40)
					}
					payload := body
					if tc.tampered {
						payload = []byte("untrusted data\n")
					}
					return testJSON(t, map[string]any{"sha": sha, "encoding": "base64", "content": base64.StdEncoding.EncodeToString(payload), "size": len(payload)}), nil
				default:
					t.Fatalf("unexpected repository request %s", r.URL)
					return nil, nil
				}
			})}
			data, err := client.ReadRepositoryFile(context.Background(), "o/r", repositoryTestRevision, "policy.md")
			if tc.allow {
				if err != nil || string(data) != string(body) || treeReads != 1 || blobReads != 1 {
					t.Fatalf("regular immutable blob not read: data=%q trees=%d blobs=%d err=%v", data, treeReads, blobReads, err)
				}
			} else if err == nil || data != nil {
				t.Fatalf("unconfined object accepted: data=%q err=%v", data, err)
			}
			if !tc.allow && (tc.mode != "100644" || tc.truncated || tc.wrongCommit || tc.wrongTree || tc.duplicate) && blobReads != 0 {
				t.Fatal("invalid tree authority reached blob endpoint")
			}
		})
	}
}

func TestGitHubRepositoryFileRejectsSymlinkAncestor(t *testing.T) {
	blobs := 0
	tree := strings.Repeat("b", 40)
	c := NewGitHubClient("http://agent.test", "token")
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/repos/o/r/git/commits/" + repositoryTestRevision:
			return testJSON(t, map[string]any{"sha": repositoryTestRevision, "tree": map[string]any{"sha": tree}}), nil
		case "/repos/o/r/git/trees/" + tree:
			return testJSON(t, map[string]any{"sha": tree, "truncated": false, "tree": []any{map[string]any{"path": ".7review", "mode": "120000", "type": "blob", "sha": strings.Repeat("c", 40)}}}), nil
		default:
			blobs++
			return testResponse(200, "outside content"), nil
		}
	})}
	if data, err := c.ReadRepositoryFile(context.Background(), "o/r", repositoryTestRevision, ".7review/review.yaml"); err == nil || data != nil || blobs != 0 {
		t.Fatalf("ancestor symlink traversed: data=%q blob_requests=%d err=%v", data, blobs, err)
	}
}
