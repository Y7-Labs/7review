package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrozenRepositoryReadRejectsNonRegularObjects(t *testing.T) {
	repo, base, _ := gitFixture(t)
	ciPath := ".7review/link.md"
	if err := os.Symlink("---\nname: link\ndescription: Symlink text is not a method.\n---\nDo not authorize this as a regular file.\n", filepath.Join(repo, filepath.FromSlash(ciPath))); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "symlink object")
	head := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	g := FrozenGitSCM{RepositoryDir: repo, RepositoryID: "org/repo", Provider: "github", BaseRevision: base, HeadRevision: head, Offline: true}
	for _, path := range []string{ciPath, ".7review"} {
		if data, err := g.ReadRepositoryFile(context.Background(), "org/repo", head, path); err == nil || data != nil {
			t.Fatalf("non-regular Git object exposed as policy bytes: path=%s data=%q err=%v", path, data, err)
		}
	}
}

func TestFrozenRepositoryReadIgnoresReplacementObjects(t *testing.T) {
	repo, base, head := gitFixture(t)
	old := strings.TrimSpace(gitRun(t, repo, "rev-parse", base+":.7review/review.yaml"))
	newID := strings.TrimSpace(gitRun(t, repo, "rev-parse", head+":.7review/review.yaml"))
	gitRun(t, repo, "replace", old, newID)
	g := FrozenGitSCM{RepositoryDir: repo, RepositoryID: "org/repo", Provider: "github", BaseRevision: base, HeadRevision: head, Offline: true}
	data, err := g.ReadRepositoryFile(context.Background(), "org/repo", base, ".7review/review.yaml")
	if err != nil || string(data) != "schema_version: 2\n" {
		t.Fatalf("replacement changed immutable base bytes: data=%q err=%v", data, err)
	}
}

func TestFrozenRepositoryReadIgnoresAmbientRepositoryOverrides(t *testing.T) {
	repo, base, head := gitFixture(t)
	foreign := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(foreign, "missing.git"))
	t.Setenv("GIT_WORK_TREE", foreign)
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(foreign, "objects"))
	g := FrozenGitSCM{RepositoryDir: repo, RepositoryID: "org/repo", Provider: "github", BaseRevision: base, HeadRevision: head, Offline: true}
	data, err := g.ReadRepositoryFile(context.Background(), "org/repo", base, ".7review/review.yaml")
	if err != nil || string(data) != "schema_version: 2\n" {
		t.Fatalf("ambient Git environment redirected frozen read: data=%q err=%v", data, err)
	}
}

func TestFrozenRepositoryReadRejectsSubmoduleAndPreservesMissingClassification(t *testing.T) {
	repo, base, head := gitFixture(t)
	gitRun(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",submodule")
	gitRun(t, repo, "commit", "-m", "submodule entry")
	head = strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	g := FrozenGitSCM{RepositoryDir: repo, RepositoryID: "org/repo", Provider: "github", BaseRevision: base, HeadRevision: head, Offline: true}
	if _, err := g.ReadRepositoryFile(context.Background(), "org/repo", head, "submodule"); err == nil || !strings.Contains(err.Error(), "regular blob") {
		t.Fatalf("submodule exposed as file: %v", err)
	}
	if _, err := g.ReadRepositoryFile(context.Background(), "org/repo", base, ".7review/missing.yaml"); !errors.Is(err, ErrRepositoryFileNotFound) {
		t.Fatalf("missing-file classification lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.ReadRepositoryFile(ctx, "org/repo", base, ".7review/review.yaml"); err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestFrozenSnapshotIgnoresCommitReplacement(t *testing.T) {
	repo, base, head := gitFixture(t)
	g := FrozenGitSCM{RepositoryDir: repo, RepositoryID: "org/repo", Provider: "github", BaseRevision: base, HeadRevision: head, Offline: true}
	before, err := g.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "replace", head, base)
	after, err := g.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.FileManifestDigest != after.FileManifestDigest {
		t.Fatal("commit replacement changed attested frozen manifest")
	}
}
