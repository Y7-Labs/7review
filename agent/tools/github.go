package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

const gitHubBotMarker = "<!-- 7review:bot-report"

type GitHubClient struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func NewGitHubClient(baseURL, token string) *GitHubClient {
	return &GitHubClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: http.DefaultClient,
	}
}

func (c *GitHubClient) Enrich(ctx context.Context, req review.Request) (*review.SCMContext, error) {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return NoopSCM{}.Enrich(ctx, req)
	}
	repo := firstNonEmpty(req.Repository, req.ProjectID)
	number := firstNonZero(req.MRIID, atoi(req.ChangeID))
	if repo == "" || number == 0 {
		return nil, fmt.Errorf("github: repository and pull request number are required")
	}

	var pr gitHubPR
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repoPath(repo), number), &pr); err != nil {
		return nil, err
	}

	var files []gitHubFile
	if err := c.getAll(ctx, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", repoPath(repo), number), &files); err != nil {
		return nil, err
	}

	var commits []gitHubCommit
	_ = c.getAll(ctx, fmt.Sprintf("/repos/%s/pulls/%d/commits?per_page=100", repoPath(repo), number), &commits)

	changedFiles := make([]review.ChangedFile, 0, len(files))
	for _, file := range files {
		changedFiles = append(changedFiles, review.ChangedFile{
			OldPath:   firstNonEmpty(file.PreviousFilename, file.Filename),
			NewPath:   file.Filename,
			Patch:     file.Patch,
			Status:    file.Status,
			Additions: file.Additions,
			Deletions: file.Deletions,
		})
	}

	normalizedCommits := make([]review.Commit, 0, len(commits))
	for _, commit := range commits {
		normalizedCommits = append(normalizedCommits, review.Commit{
			SHA:     commit.SHA,
			Title:   firstLine(commit.Commit.Message),
			Message: commit.Commit.Message,
			Author:  commit.Commit.Author.Name,
		})
	}

	labels := make([]string, 0, len(pr.Labels))
	for _, label := range pr.Labels {
		labels = append(labels, label.Name)
	}

	return &review.SCMContext{
		Provider:    "github",
		ProjectID:   repo,
		Repository:  repo,
		ChangeID:    strconv.Itoa(number),
		MRIID:       number,
		Title:       firstNonEmpty(pr.Title, req.Title),
		Description: firstNonEmpty(pr.Body, req.Description),
		Author:      firstNonEmpty(pr.User.Login, req.Author),
		WebURL:      firstNonEmpty(pr.HTMLURL, req.WebURL),
		Labels:      labels,
		DiffRefs: review.DiffRefs{
			BaseSHA: firstNonEmpty(pr.Base.SHA, req.TargetSHA),
			HeadSHA: firstNonEmpty(pr.Head.SHA, req.SourceSHA),
		},
		Commits: normalizedCommits,
		Files:   changedFiles,
	}, nil
}

func (c *GitHubClient) ReadRepositoryFile(ctx context.Context, repositoryID, revision, filePath string) ([]byte, error) {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return nil, fmt.Errorf("github: repository file reader is not configured")
	}
	if repositoryID == "" || revision == "" || filePath == "" {
		return nil, fmt.Errorf("github: repository, revision and path are required")
	}
	var content struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	endpoint := fmt.Sprintf("/repos/%s/contents/%s?ref=%s", repoPath(repositoryID), escapeRepositoryPath(filePath), url.QueryEscape(revision))
	if err := c.get(ctx, endpoint, &content); err != nil {
		if isHTTPStatus(err, http.StatusNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrRepositoryFileNotFound, filePath)
		}
		return nil, err
	}
	if content.Type != "file" || content.Encoding != "base64" {
		return nil, fmt.Errorf("github: %s is not a base64 file", filePath)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("github: decode %s: %w", filePath, err)
	}
	if len(decoded) > repositoryFileReadLimit {
		return nil, fmt.Errorf("github: repository file exceeds %d bytes", repositoryFileReadLimit)
	}
	return decoded, nil
}

func (c *GitHubClient) PublishDraft(ctx context.Context, source *review.SCMContext, report string) error {
	return c.upsertComment(ctx, source, report, "draft")
}

func (c *GitHubClient) PublishFinal(ctx context.Context, source *review.SCMContext, report string) error {
	return c.upsertComment(ctx, source, report, "final")
}

func (c *GitHubClient) PublishInlineDraft(ctx context.Context, source *review.SCMContext, comment review.InlineComment) (review.InlineComment, error) {
	if c == nil || c.BaseURL == "" || c.Token == "" || source == nil {
		comment.Status = "skipped"
		comment.Reason = "github publisher is not configured"
		return comment, nil
	}
	repo := firstNonEmpty(source.Repository, source.ProjectID)
	if repo == "" || source.MRIID == 0 {
		comment.Status = "failed"
		comment.Reason = "github repository and pull request number are required"
		return comment, fmt.Errorf("%s", comment.Reason)
	}
	commentsPath := fmt.Sprintf("/repos/%s/pulls/%d/comments", repoPath(repo), source.MRIID)
	var comments []gitHubReviewComment
	if err := c.getAll(ctx, commentsPath+"?per_page=100", &comments); err != nil {
		comment.Status = "failed"
		comment.Reason = err.Error()
		return comment, err
	}
	for _, existing := range comments {
		if hasInlineCommentMarker(existing.Body, comment.FindingID) {
			comment.Status = "published"
			comment.ProviderID = strconv.FormatInt(existing.ID, 10)
			comment.URL = existing.HTMLURL
			return comment, nil
		}
	}
	payload := map[string]any{
		"body":      inlineCommentBody(comment.Body, comment.FindingID),
		"commit_id": source.DiffRefs.HeadSHA,
		"path":      firstNonEmpty(comment.NewPath, comment.Path),
		"line":      comment.Line,
		"side":      firstNonEmpty(comment.Side, "RIGHT"),
	}
	var out gitHubReviewComment
	if err := c.send(ctx, http.MethodPost, commentsPath, payload, &out); err != nil {
		comment.Status = "failed"
		comment.Reason = err.Error()
		return comment, err
	}
	comment.Status = "published"
	if out.ID != 0 {
		comment.ProviderID = strconv.FormatInt(out.ID, 10)
	}
	comment.URL = out.HTMLURL
	return comment, nil
}

func (c *GitHubClient) upsertComment(ctx context.Context, source *review.SCMContext, body string, kind string) error {
	if c == nil || c.BaseURL == "" || c.Token == "" || source == nil {
		return nil
	}
	repo := firstNonEmpty(source.Repository, source.ProjectID)
	if repo == "" || source.MRIID == 0 {
		return fmt.Errorf("github: repository and pull request number are required")
	}
	body = reportWithBotMarker(body, kind)
	commentsPath := fmt.Sprintf("/repos/%s/issues/%d/comments", repoPath(repo), source.MRIID)
	var comments []gitHubComment
	if err := c.getAll(ctx, commentsPath+"?per_page=100", &comments); err != nil {
		return err
	}
	for _, comment := range comments {
		if hasBotMarkerKind(comment.Body, kind) || (kind == "draft" && hasLegacyBotMarker(comment.Body)) {
			return c.send(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repoPath(repo), comment.ID), map[string]string{"body": body}, nil)
		}
	}
	return c.send(ctx, http.MethodPost, commentsPath, map[string]string{"body": body}, nil)
}

func (c *GitHubClient) get(ctx context.Context, path string, out any) error {
	return c.send(ctx, http.MethodGet, path, nil, out)
}

func (c *GitHubClient) getAll(ctx context.Context, path string, out any) error {
	return fetchPages(ctx, path, out, c.sendPage, nextGitHubPage)
}

func (c *GitHubClient) send(ctx context.Context, method, path string, in any, out any) error {
	_, err := c.sendPage(ctx, method, path, in, out)
	return err
}

func (c *GitHubClient) sendPage(ctx context.Context, method, path string, in any, out any) (http.Header, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.requestURL(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &providerHTTPError{service: "github", method: method, path: path, status: resp.Status, code: resp.StatusCode, body: readToolErrorBody(resp.Body)}
	}
	if out == nil {
		return resp.Header.Clone(), nil
	}
	return resp.Header.Clone(), decodeToolJSON("github", method, path, resp.Body, out)
}

func (c *GitHubClient) requestURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	return c.BaseURL + path
}

func repoPath(repo string) string {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return url.PathEscape(repo)
	}
	return url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func escapeRepositoryPath(value string) string {
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func atoi(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}

type gitHubPR struct {
	Title   string `json:"title"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
	} `json:"base"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type gitHubFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Patch            string `json:"patch"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
}

type gitHubCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"commit"`
}

type gitHubComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type gitHubReviewComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}
