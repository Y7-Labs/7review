package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/review"
)

func TestFrozenGitSCMReadsBasePolicyAndFrozenDiff(t *testing.T) {
	repo, base, head := gitFixture(t)
	adapter := FrozenGitSCM{
		RepositoryDir: repo, RepositoryID: "org/repo", Provider: "github",
		BaseRevision: base, HeadRevision: head, Offline: true,
	}
	snapshot, err := adapter.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AnalysisRevision() != head || !strings.HasPrefix(snapshot.FileManifestDigest, "sha256:") {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	policyData, err := adapter.ReadRepositoryFile(context.Background(), "org/repo", base, ".7review/review.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(policyData) != "schema_version: 2\n" {
		t.Fatalf("policy was not read from base: %q", policyData)
	}
	scm, err := adapter.Enrich(context.Background(), review.Request{ProjectID: "org/repo", TargetSHA: base, SourceSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	if len(scm.Files) != 2 || !changedFileExists(scm.Files, "service.go", "added") || !changedFileExists(scm.Files, ".7review/review.yaml", "modified") {
		t.Fatalf("unexpected frozen changes: %#v", scm.Files)
	}

	if err := os.WriteFile(filepath.Join(repo, "service.go"), []byte("working tree mutation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scmAfterMutation, err := adapter.Enrich(context.Background(), review.Request{ProjectID: "org/repo", TargetSHA: base, SourceSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	if scmAfterMutation.Files[0].Patch != scm.Files[0].Patch || scmAfterMutation.Files[1].Patch != scm.Files[1].Patch {
		t.Fatal("working tree mutation changed the frozen diff")
	}
}

func TestFrozenGitSCMBindsSyntheticMerge(t *testing.T) {
	repo, base, head := gitFixture(t)
	gitRun(t, repo, "checkout", "-b", "target", base)
	writeGitFile(t, repo, "target.txt", "target\n")
	gitRun(t, repo, "add", "target.txt")
	gitRun(t, repo, "commit", "-m", "target")
	gitRun(t, repo, "merge", "--no-ff", "--no-edit", head)
	merge := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	adapter := FrozenGitSCM{
		RepositoryDir: repo, RepositoryID: "org/repo", Provider: "github",
		BaseRevision: base, HeadRevision: head, ComparisonRevision: merge, Offline: true,
	}
	snapshot, err := adapter.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SyntheticMergeRevision != merge || snapshot.AnalysisRevision() != merge || snapshot.SourceToMergeMappingDigest == "" {
		t.Fatalf("synthetic merge identity was lost: %#v", snapshot)
	}
}

func TestFrozenGitSCMRejectsMissingRevisionOfflineAndUnsafePath(t *testing.T) {
	repo, base, head := gitFixture(t)
	adapter := FrozenGitSCM{RepositoryDir: repo, RepositoryID: "org/repo", Provider: "github", BaseRevision: base, HeadRevision: strings.Repeat("f", 40), Offline: true}
	if _, err := adapter.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "offline mode") {
		t.Fatalf("missing offline revision must fail: %v", err)
	}
	adapter.HeadRevision = head
	if _, err := adapter.ReadRepositoryFile(context.Background(), "org/repo", base, "../secret"); err == nil || !strings.Contains(err.Error(), "repository-relative") {
		t.Fatalf("unsafe repository path must fail: %v", err)
	}
}

func gitFixture(t *testing.T) (string, string, string) {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "user.name", "7review test")
	gitRun(t, repo, "config", "user.email", "test@example.com")
	writeGitFile(t, repo, ".7review/review.yaml", "schema_version: 2\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	writeGitFile(t, repo, ".7review/review.yaml", "schema_version: 2\nproposed: true\n")
	writeGitFile(t, repo, "service.go", "package service\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "head")
	head := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	return repo, base, head
}

func writeGitFile(t *testing.T, repo, relativePath, content string) {
	t.Helper()
	fullPath := filepath.Join(repo, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, repo string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func changedFileExists(files []review.ChangedFile, path, status string) bool {
	for _, file := range files {
		if file.NewPath == path && file.Status == status {
			return true
		}
	}
	return false
}
