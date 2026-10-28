package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxRepositoryReadRequests = 128
const maxRepositoryReadBytes = 16 << 20

var repositoryNameSegment = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validateRepositoryRead(provider, repository, revision, filePath string) error {
	parts := strings.Split(repository, "/")
	if len(repository) == 0 || len(repository) > 256 || (provider == "github" && len(parts) != 2) {
		return fmt.Errorf("%s: invalid repository identity", provider)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || !repositoryNameSegment.MatchString(part) {
			return fmt.Errorf("%s: invalid repository identity", provider)
		}
	}
	if !immutableGitRevisionPattern.MatchString(revision) {
		return fmt.Errorf("%s: repository revision must be a full immutable SHA", provider)
	}
	return validateRepositoryReadPath(filePath)
}

func validateRepositoryReadPath(filePath string) error {
	if !validRepositoryRelativePath(filePath) || filePath == ".." || len(filePath) > 4096 || len(strings.Split(filePath, "/")) > 32 || !utf8.ValidString(filePath) || strings.ContainsAny(filePath, "%?#:") {
		return fmt.Errorf("repository file path must be canonical and repository-relative")
	}
	for _, r := range filePath {
		if unicode.IsControl(r) {
			return fmt.Errorf("repository file path contains control characters")
		}
	}
	return nil
}

type repositoryHTTP struct {
	service, baseURL, authHeader, authValue string
	client                                  http.Client
	requests, bytes                         int
}

func newRepositoryHTTP(service, baseURL, authHeader, authValue string, client *http.Client) (*repositoryHTTP, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%s: invalid repository API base URL", service)
	}
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &repositoryHTTP{service: service, baseURL: strings.TrimRight(baseURL, "/"), authHeader: authHeader, authValue: authValue, client: copyClient}, nil
}

func (r *repositoryHTTP) get(ctx context.Context, endpoint string, limit int64) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	r.requests++
	if r.requests > maxRepositoryReadRequests {
		return nil, nil, fmt.Errorf("%s: repository read request bound exceeded", r.service)
	}
	if !strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, "//") {
		return nil, nil, fmt.Errorf("%s: invalid repository API endpoint", r.service)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+endpoint, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set(r.authHeader, r.authValue)
	req.Header.Set("Accept", "application/vnd.github+json")
	if r.service == "github" {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, &providerHTTPError{service: r.service, method: http.MethodGet, path: endpoint, status: resp.Status, code: resp.StatusCode, body: readToolErrorBody(resp.Body)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > limit {
		return nil, nil, fmt.Errorf("%s: repository response exceeds byte bound", r.service)
	}
	r.bytes += len(data)
	if r.bytes > maxRepositoryReadBytes {
		return nil, nil, fmt.Errorf("%s: repository read aggregate byte bound exceeded", r.service)
	}
	return data, resp.Header.Clone(), nil
}
