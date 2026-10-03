package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func TestGitHubCIArtifactVerifierBindsRunRevisionAndDigest(t *testing.T) {
	payload := qualityArtifactFixture("tests", "passed")
	archive := zipFixture(t, "quality/tests.json", payload)
	metadataDigest := digestBytes(archive)
	expired := false
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/actions/artifacts/41":
			fmt.Fprintf(w, `{"id":41,"expired":%t,"digest":%q,"archive_download_url":%q,"workflow_run":{"id":99,"head_sha":"head"}}`, expired, metadataDigest, server.URL+"/archive.zip")
		case "/archive.zip":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snapshot, execution := artifactIdentityFixture()
	verifier := GitHubCIArtifactVerifier{BaseURL: server.URL, Repository: "org/repo", Client: server.Client()}
	imported, err := verifier.VerifyAndImport(context.Background(), 41, "quality/tests.json", snapshot, execution)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Check.ID != "tests" || imported.Provenance.PipelineID != "99" || imported.Provenance.ProviderArchiveDigest != digestBytes(archive) {
		t.Fatalf("github artifact provenance was not preserved: %#v", imported)
	}

	metadataDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := verifier.VerifyAndImport(context.Background(), 41, "quality/tests.json", snapshot, execution); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("github archive digest mismatch must fail: %v", err)
	}
	metadataDigest = digestBytes(archive)
	expired = true
	if _, err := verifier.VerifyAndImport(context.Background(), 41, "quality/tests.json", snapshot, execution); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired github artifact must fail: %v", err)
	}
	expired = false
	execution.PipelineID = "100"
	if _, err := verifier.VerifyAndImport(context.Background(), 41, "quality/tests.json", snapshot, execution); err == nil || !strings.Contains(err.Error(), "another workflow run") {
		t.Fatalf("wrong workflow run must fail: %v", err)
	}
}

func TestGitLabCIArtifactVerifierBindsJobPipelineAndRevision(t *testing.T) {
	payload := qualityArtifactFixture("security", "failed")
	archive := zipFixture(t, "quality/security.json", payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/123/jobs/77":
			fmt.Fprint(w, `{"id":77,"status":"failed","pipeline":{"id":99,"sha":"head"}}`)
		case "/api/v4/projects/123/jobs/77/artifacts":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snapshot, execution := artifactIdentityFixture()
	verifier := GitLabCIArtifactVerifier{BaseURL: server.URL, ProjectID: "123", JobToken: "job-token", Client: server.Client()}
	imported, err := verifier.VerifyAndImport(context.Background(), 77, "quality/security.json", snapshot, execution)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Check.Status != review.CheckViolated || imported.Provenance.JobID != "77" || imported.Provenance.ProviderArchiveDigest != digestBytes(archive) {
		t.Fatalf("gitlab artifact provenance was not preserved: %#v", imported)
	}
	execution.ComparisonTree = "other"
	snapshot.HeadRevision = "source"
	if _, err := verifier.VerifyAndImport(context.Background(), 77, "quality/security.json", snapshot, execution); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("wrong gitlab pipeline revision must fail: %v", err)
	}
}

func TestGitLabCIArtifactVerifierRejectsExpiredArtifact(t *testing.T) {
	payload := qualityArtifactFixture("tests", "passed")
	archive := zipFixture(t, "quality/tests.json", payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/jobs/77"):
			fmt.Fprint(w, `{"id":77,"status":"success","artifacts_expire_at":"2026-10-03T11:00:00Z","pipeline":{"id":99,"sha":"head"}}`)
		case strings.HasSuffix(r.URL.Path, "/jobs/77/artifacts"):
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	snapshot, execution := artifactIdentityFixture()
	verifier := GitLabCIArtifactVerifier{BaseURL: server.URL, ProjectID: "123", Client: server.Client(), Now: func() time.Time {
		return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	}}
	if _, err := verifier.VerifyAndImport(context.Background(), 77, "quality/tests.json", snapshot, execution); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired gitlab artifact must fail: %v", err)
	}
}

func TestExtractArtifactFileRejectsOversizedEntry(t *testing.T) {
	archive := zipFixture(t, "quality.json", bytes.Repeat([]byte("x"), review.MaxCIQualityArtifactBytes+1))
	if _, err := extractArtifactFile(archive, "quality.json"); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized extracted artifact must fail: %v", err)
	}
}

func artifactIdentityFixture() (review.SnapshotIdentity, review.ExecutionContext) {
	now := time.Now().UTC()
	return review.SnapshotIdentity{
			RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head",
			FileManifestDigest: "sha256:" + strings.Repeat("a", 64),
		}, review.ExecutionContext{
			Mode: review.ExecutionEphemeral, ProducerID: "ci-runner", PipelineID: "99", JobID: "runner",
			ComparisonTree: "head", Persistence: review.PersistenceJobLocal,
			TrustedBoundaryID: "ci:org/repo:99", DeadlineAt: now.Add(time.Hour),
		}
}

func qualityArtifactFixture(checkID, status string) []byte {
	signals := "[]"
	if status == "failed" {
		signals = `[{"key":"security:1","rule_id":"unsafe-input","severity":"high","strength":"confirmed","message":"unsafe input","in_changed_scope":true}]`
	}
	return []byte(fmt.Sprintf(`{"schema_version":1,"kind":"test","check_id":%q,"method_id":"ci/test","scope":"project","status":%q,"summary":"fixture","signals":%s}`, checkID, status, signals))
}

func zipFixture(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
