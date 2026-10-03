package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

const maxCIArtifactArchiveBytes = 64 << 20

type GitHubCIArtifactVerifier struct {
	BaseURL    string
	Token      string
	Repository string
	Client     *http.Client
	Now        func() time.Time
}

func (v GitHubCIArtifactVerifier) VerifyAndImport(ctx context.Context, artifactID int64, artifactPath string, snapshot review.SnapshotIdentity, execution review.ExecutionContext) (review.ImportedCIQualityEvidence, error) {
	if artifactID <= 0 || !validRepositoryRelativePath(artifactPath) {
		return review.ImportedCIQualityEvidence{}, errors.New("github CI artifact requires a positive ID and repository-relative path")
	}
	if err := snapshot.Validate(); err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	if err := execution.Validate(); err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	if execution.PipelineID == "" {
		return review.ImportedCIQualityEvidence{}, errors.New("github CI artifact requires a workflow run ID")
	}
	baseURL := strings.TrimRight(v.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	endpoint := fmt.Sprintf("%s/repos/%s/actions/artifacts/%d", baseURL, strings.Trim(v.Repository, "/"), artifactID)
	var metadata struct {
		ID                 int64  `json:"id"`
		Expired            bool   `json:"expired"`
		Digest             string `json:"digest"`
		ArchiveDownloadURL string `json:"archive_download_url"`
		WorkflowRun        struct {
			ID      int64  `json:"id"`
			HeadSHA string `json:"head_sha"`
		} `json:"workflow_run"`
	}
	if err := v.getJSON(ctx, endpoint, &metadata); err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	if metadata.ID != artifactID || metadata.Expired {
		return review.ImportedCIQualityEvidence{}, errors.New("github CI artifact is missing, expired or has a mismatched identity")
	}
	if strconv.FormatInt(metadata.WorkflowRun.ID, 10) != execution.PipelineID {
		return review.ImportedCIQualityEvidence{}, errors.New("github CI artifact belongs to another workflow run")
	}
	if metadata.WorkflowRun.HeadSHA != execution.ComparisonTree && metadata.WorkflowRun.HeadSHA != snapshot.HeadRevision {
		return review.ImportedCIQualityEvidence{}, errors.New("github CI artifact workflow revision does not match the frozen comparison")
	}
	if !strings.HasPrefix(metadata.Digest, "sha256:") || metadata.ArchiveDownloadURL == "" {
		return review.ImportedCIQualityEvidence{}, errors.New("github CI artifact lacks digest or download URL")
	}
	archive, err := v.download(ctx, metadata.ArchiveDownloadURL)
	if err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	if digestBytes(archive) != metadata.Digest {
		return review.ImportedCIQualityEvidence{}, errors.New("github CI artifact archive digest mismatch")
	}
	payload, err := extractArtifactFile(archive, artifactPath)
	if err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	jobID := "artifact:" + strconv.FormatInt(artifactID, 10)
	provenance := review.CIArtifactProvenance{
		PipelineID: execution.PipelineID, JobID: jobID, SourceRevision: snapshot.HeadRevision,
		ComparisonTree: execution.ComparisonTree, ProviderArtifactID: strconv.FormatInt(artifactID, 10),
		ProviderArchiveDigest: metadata.Digest, ArtifactDigest: digestBytes(payload),
		ProducerID: "github-actions", VerifiedAt: now, ProviderVerified: true,
	}
	artifactExecution := execution
	artifactExecution.JobID = jobID
	return review.ImportCIQualityEvidence(payload, provenance, snapshot, artifactExecution)
}

func (v GitHubCIArtifactVerifier) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if v.Token != "" {
		request.Header.Set("Authorization", "Bearer "+v.Token)
	}
	response, err := httpClient(v.Client).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("github artifact metadata: %s: %s", response.Status, readToolErrorBody(response.Body))
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("github artifact metadata: %w", err)
	}
	return nil
}

func (v GitHubCIArtifactVerifier) download(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if v.Token != "" {
		request.Header.Set("Authorization", "Bearer "+v.Token)
	}
	return downloadBounded(httpClient(v.Client), request)
}

type GitLabCIArtifactVerifier struct {
	BaseURL   string
	Token     string
	JobToken  string
	ProjectID string
	Client    *http.Client
	Now       func() time.Time
}

func (v GitLabCIArtifactVerifier) VerifyAndImport(ctx context.Context, jobID int64, artifactPath string, snapshot review.SnapshotIdentity, execution review.ExecutionContext) (review.ImportedCIQualityEvidence, error) {
	if jobID <= 0 || !validRepositoryRelativePath(artifactPath) {
		return review.ImportedCIQualityEvidence{}, errors.New("gitlab CI artifact requires a positive job ID and repository-relative path")
	}
	if err := snapshot.Validate(); err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	if err := execution.Validate(); err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	baseURL := strings.TrimRight(v.BaseURL, "/")
	if baseURL == "" || v.ProjectID == "" || execution.PipelineID == "" {
		return review.ImportedCIQualityEvidence{}, errors.New("gitlab CI artifact requires base URL, project and pipeline")
	}
	jobEndpoint := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d", baseURL, url.PathEscape(v.ProjectID), jobID)
	var metadata struct {
		ID                int64      `json:"id"`
		Status            string     `json:"status"`
		ArtifactsExpireAt *time.Time `json:"artifacts_expire_at"`
		Pipeline          struct {
			ID  int64  `json:"id"`
			SHA string `json:"sha"`
		} `json:"pipeline"`
	}
	if err := v.getJSON(ctx, jobEndpoint, &metadata); err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	if metadata.ID != jobID || (metadata.Status != "success" && metadata.Status != "failed") {
		return review.ImportedCIQualityEvidence{}, errors.New("gitlab CI artifact job is not a completed producer")
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	if metadata.ArtifactsExpireAt != nil && !now.Before(metadata.ArtifactsExpireAt.UTC()) {
		return review.ImportedCIQualityEvidence{}, errors.New("gitlab CI artifact is expired")
	}
	if strconv.FormatInt(metadata.Pipeline.ID, 10) != execution.PipelineID {
		return review.ImportedCIQualityEvidence{}, errors.New("gitlab CI artifact belongs to another pipeline")
	}
	if metadata.Pipeline.SHA != execution.ComparisonTree && metadata.Pipeline.SHA != snapshot.HeadRevision {
		return review.ImportedCIQualityEvidence{}, errors.New("gitlab CI artifact pipeline revision does not match the frozen comparison")
	}
	archiveEndpoint := fmt.Sprintf("%s/api/v4/projects/%s/jobs/%d/artifacts", baseURL, url.PathEscape(v.ProjectID), jobID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveEndpoint, nil)
	if err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	v.authorize(request)
	archive, err := downloadBounded(httpClient(v.Client), request)
	if err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	payload, err := extractArtifactFile(archive, artifactPath)
	if err != nil {
		return review.ImportedCIQualityEvidence{}, err
	}
	provenance := review.CIArtifactProvenance{
		PipelineID: execution.PipelineID, JobID: strconv.FormatInt(jobID, 10), SourceRevision: snapshot.HeadRevision,
		ComparisonTree: execution.ComparisonTree, ProviderArtifactID: strconv.FormatInt(jobID, 10),
		ProviderArchiveDigest: digestBytes(archive), ArtifactDigest: digestBytes(payload),
		ProducerID: "gitlab-ci", VerifiedAt: now, ProviderVerified: true,
	}
	artifactExecution := execution
	artifactExecution.JobID = provenance.JobID
	return review.ImportCIQualityEvidence(payload, provenance, snapshot, artifactExecution)
}

func (v GitLabCIArtifactVerifier) getJSON(ctx context.Context, endpoint string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	v.authorize(request)
	response, err := httpClient(v.Client).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("gitlab artifact metadata: %s: %s", response.Status, readToolErrorBody(response.Body))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("gitlab artifact metadata: %w", err)
	}
	return nil
}

func (v GitLabCIArtifactVerifier) authorize(request *http.Request) {
	if v.JobToken != "" {
		request.Header.Set("JOB-TOKEN", v.JobToken)
	} else if v.Token != "" {
		request.Header.Set("PRIVATE-TOKEN", v.Token)
	}
}

func downloadBounded(client *http.Client, request *http.Request) ([]byte, error) {
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("CI artifact download: %s: %s", response.Status, readToolErrorBody(response.Body))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCIArtifactArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCIArtifactArchiveBytes {
		return nil, fmt.Errorf("CI artifact archive exceeds %d bytes", maxCIArtifactArchiveBytes)
	}
	return data, nil
}

func extractArtifactFile(archive []byte, artifactPath string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("open CI artifact archive: %w", err)
	}
	for _, file := range reader.File {
		if file.Name != artifactPath {
			continue
		}
		if file.FileInfo().IsDir() || file.UncompressedSize64 > review.MaxCIQualityArtifactBytes {
			return nil, errors.New("CI quality artifact file is a directory or exceeds its size limit")
		}
		stream, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(stream, review.MaxCIQualityArtifactBytes+1))
		closeErr := stream.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(data) > review.MaxCIQualityArtifactBytes {
			return nil, errors.New("CI quality artifact file exceeds its size limit")
		}
		return data, nil
	}
	return nil, fmt.Errorf("CI quality artifact path %q not found in archive", artifactPath)
}

func httpClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return http.DefaultClient
}
