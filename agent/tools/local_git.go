package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

const (
	maxGitMetadataBytes = 8 << 20
	maxGitDiffBytes     = 32 << 20
	maxGitChangedFiles  = 4096
)

var immutableGitRevisionPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

type FrozenGitSCM struct {
	RepositoryDir      string
	RepositoryID       string
	Provider           string
	BaseRevision       string
	HeadRevision       string
	ComparisonRevision string
	Remote             string
	Offline            bool
}

func (g FrozenGitSCM) Snapshot(ctx context.Context) (review.SnapshotIdentity, error) {
	if err := g.validate(); err != nil {
		return review.SnapshotIdentity{}, err
	}
	for _, revision := range []string{g.BaseRevision, g.HeadRevision, g.comparisonRevision()} {
		if err := g.ensureRevision(ctx, revision); err != nil {
			return review.SnapshotIdentity{}, err
		}
	}
	manifest, err := g.run(ctx, maxGitMetadataBytes, "ls-tree", "-r", "-z", "--full-tree", g.comparisonRevision())
	if err != nil {
		return review.SnapshotIdentity{}, fmt.Errorf("git manifest: %w", err)
	}
	snapshot := review.SnapshotIdentity{
		RepositoryID: g.RepositoryID, BaseRevision: g.BaseRevision, HeadRevision: g.HeadRevision,
		FileManifestDigest: digestBytes(manifest),
	}
	if comparison := g.comparisonRevision(); comparison != g.HeadRevision {
		mapping, err := g.run(ctx, maxGitDiffBytes, "diff", "--binary", "--no-ext-diff", g.HeadRevision, comparison, "--")
		if err != nil {
			return review.SnapshotIdentity{}, fmt.Errorf("git source-to-merge mapping: %w", err)
		}
		snapshot.SyntheticMergeRevision = comparison
		snapshot.SourceToMergeMappingDigest = digestBytes(mapping)
	}
	if err := snapshot.Validate(); err != nil {
		return review.SnapshotIdentity{}, err
	}
	return snapshot, nil
}

func (g FrozenGitSCM) Enrich(ctx context.Context, req review.Request) (*review.SCMContext, error) {
	snapshot, err := g.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if req.ProjectID != "" && req.ProjectID != g.RepositoryID {
		return nil, fmt.Errorf("git repository identity %q does not match request %q", g.RepositoryID, req.ProjectID)
	}
	if req.TargetSHA != "" && req.TargetSHA != snapshot.BaseRevision {
		return nil, errors.New("git base revision does not match request")
	}
	if req.SourceSHA != "" && req.SourceSHA != snapshot.HeadRevision {
		return nil, errors.New("git source revision does not match request")
	}
	files, err := g.changedFiles(ctx)
	if err != nil {
		return nil, err
	}
	return &review.SCMContext{
		Provider: g.Provider, ProjectID: g.RepositoryID, Repository: g.RepositoryID,
		ChangeID: req.ChangeID, MRIID: req.MRIID, Title: req.Title, Description: req.Description,
		Author: req.Author, WebURL: req.WebURL, Labels: append([]string(nil), req.Labels...),
		DiffRefs: review.DiffRefs{BaseSHA: snapshot.BaseRevision, HeadSHA: snapshot.HeadRevision, StartSHA: snapshot.SyntheticMergeRevision},
		Files:    files,
	}, nil
}

func (g FrozenGitSCM) ReadRepositoryFile(ctx context.Context, repositoryID, revision, filePath string) ([]byte, error) {
	if err := g.validate(); err != nil {
		return nil, err
	}
	if repositoryID != g.RepositoryID {
		return nil, errors.New("git repository identity mismatch")
	}
	if revision != g.BaseRevision && revision != g.HeadRevision && revision != g.comparisonRevision() {
		return nil, errors.New("git file revision is outside the frozen snapshot")
	}
	if !validRepositoryRelativePath(filePath) {
		return nil, errors.New("git file path must be repository-relative")
	}
	if err := g.ensureRevision(ctx, revision); err != nil {
		return nil, err
	}
	data, err := g.run(ctx, repositoryFileReadLimit, "show", revision+":"+filePath)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
			return nil, fmt.Errorf("%w: %s", ErrRepositoryFileNotFound, filePath)
		}
		return nil, err
	}
	return data, nil
}

func (g FrozenGitSCM) changedFiles(ctx context.Context) ([]review.ChangedFile, error) {
	data, err := g.run(ctx, maxGitMetadataBytes, "diff", "--name-status", "-z", "--find-renames", g.BaseRevision, g.comparisonRevision(), "--")
	if err != nil {
		return nil, fmt.Errorf("git changed files: %w", err)
	}
	parts := bytes.Split(data, []byte{0})
	files := make([]review.ChangedFile, 0, len(parts)/2)
	for i := 0; i < len(parts) && len(parts[i]) > 0; {
		status := string(parts[i])
		i++
		if i >= len(parts) {
			return nil, errors.New("git changed files returned a truncated record")
		}
		file := review.ChangedFile{Status: normalizeGitStatus(status)}
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			if i+1 >= len(parts) {
				return nil, errors.New("git rename record is truncated")
			}
			file.OldPath, file.NewPath = string(parts[i]), string(parts[i+1])
			i += 2
		} else {
			file.NewPath = string(parts[i])
			i++
			if file.Status == "deleted" {
				file.OldPath = file.NewPath
			}
		}
		if len(files) >= maxGitChangedFiles {
			return nil, fmt.Errorf("git change exceeds %d files", maxGitChangedFiles)
		}
		patchPath := file.NewPath
		if file.Status == "deleted" {
			patchPath = file.OldPath
		}
		patchData, err := g.run(ctx, maxGitDiffBytes, "diff", "--binary", "--no-ext-diff", "--unified=80", g.BaseRevision, g.comparisonRevision(), "--", patchPath)
		if err != nil {
			return nil, fmt.Errorf("git patch %q: %w", patchPath, err)
		}
		file.Patch = string(patchData)
		files = append(files, file)
	}
	return files, nil
}

func (g FrozenGitSCM) validate() error {
	if strings.TrimSpace(g.RepositoryDir) == "" || strings.TrimSpace(g.RepositoryID) == "" || strings.TrimSpace(g.Provider) == "" {
		return errors.New("git repository directory, identity and provider are required")
	}
	for name, revision := range map[string]string{"base": g.BaseRevision, "head": g.HeadRevision, "comparison": g.comparisonRevision()} {
		if !immutableGitRevisionPattern.MatchString(revision) {
			return fmt.Errorf("git %s revision must be a full immutable SHA", name)
		}
	}
	return nil
}

func (g FrozenGitSCM) ensureRevision(ctx context.Context, revision string) error {
	if _, err := g.run(ctx, 1024, "cat-file", "-e", revision+"^{commit}"); err == nil {
		return nil
	}
	if g.Offline {
		return fmt.Errorf("git revision %s is unavailable in offline mode", revision)
	}
	remote := strings.TrimSpace(g.Remote)
	if remote == "" {
		remote = "origin"
	}
	if _, err := g.run(ctx, maxGitMetadataBytes, "fetch", "--no-tags", "--depth=1", remote, revision); err != nil {
		return fmt.Errorf("git fetch revision %s: %w", revision, err)
	}
	if _, err := g.run(ctx, 1024, "cat-file", "-e", revision+"^{commit}"); err != nil {
		return fmt.Errorf("git revision %s remains unavailable after fetch", revision)
	}
	return nil
}

func (g FrozenGitSCM) comparisonRevision() string {
	if strings.TrimSpace(g.ComparisonRevision) != "" {
		return g.ComparisonRevision
	}
	return g.HeadRevision
}

func (g FrozenGitSCM) run(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", g.RepositoryDir}, args...)...)
	command.Env = append(command.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr cappedWriter
	stderr.limit = 16 << 10
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	if int64(len(data)) > limit {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("git output exceeds %d bytes", limit)
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = waitErr.Error()
		}
		return nil, errors.New(message)
	}
	return data, nil
}

type cappedWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (w *cappedWriter) Write(data []byte) (int, error) {
	original := len(data)
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = w.buffer.Write(data)
	}
	return original, nil
}

func (w *cappedWriter) String() string { return w.buffer.String() }

func normalizeGitStatus(status string) string {
	switch {
	case strings.HasPrefix(status, "A"):
		return "added"
	case strings.HasPrefix(status, "D"):
		return "deleted"
	case strings.HasPrefix(status, "R"):
		return "renamed"
	case strings.HasPrefix(status, "C"):
		return "copied"
	default:
		return "modified"
	}
}

func validRepositoryRelativePath(value string) bool {
	clean := path.Clean(value)
	return value != "" && clean == value && clean != "." && !strings.HasPrefix(clean, "../") && !strings.HasPrefix(clean, "/") && !strings.Contains(value, "\\")
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
