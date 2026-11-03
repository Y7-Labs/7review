package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

type AuthorizedPublicationRequest struct {
	OperationID   string
	PayloadDigest string
	Destination   review.PublicationDestination
	Payload       string
}

type AuthorizedPublicationReceipt struct {
	Provider      string
	RemoteID      string
	RemoteURL     string
	PayloadDigest string
}

type AuthorizedPublisher interface {
	PublishAuthorized(context.Context, AuthorizedPublicationRequest) (AuthorizedPublicationReceipt, error)
	LookupAuthorized(context.Context, AuthorizedPublicationRequest) (*AuthorizedPublicationReceipt, error)
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r AuthorizedPublicationRequest) validate(provider string) (int, error) {
	if r.Destination.Validate() != nil || r.Destination.Provider != provider || strings.TrimSpace(r.OperationID) == "" || strings.ContainsAny(r.OperationID, " \t\r\n") || len(r.OperationID) > 256 || r.PayloadDigest != digestText(r.Payload) {
		return 0, errors.New("tools: invalid exact authorized publication")
	}
	change, err := strconv.Atoi(r.Destination.ChangeNumber)
	if err != nil || change <= 0 {
		return 0, errors.New("tools: invalid publication change number")
	}
	return change, nil
}

func (r AuthorizedPublicationRequest) validateEndpoint(provider, endpoint string) (int, error) {
	change, err := r.validate(provider)
	if err != nil {
		return 0, err
	}
	host, err := review.ProviderHost(provider, endpoint)
	if err != nil || host != r.Destination.Host {
		return 0, errors.New("tools: authorized publication endpoint host changed")
	}
	return change, nil
}

func authorizedPublicationBody(r AuthorizedPublicationRequest) string {
	marker := fmt.Sprintf("%s kind=final operation=%s payload=%s -->", botMarkerPrefix, r.OperationID, r.PayloadDigest)
	if r.Payload == "" {
		return marker
	}
	return marker + "\n" + r.Payload
}

func authorizedReceipt(provider, remoteID, remoteURL string, r AuthorizedPublicationRequest) AuthorizedPublicationReceipt {
	return AuthorizedPublicationReceipt{Provider: provider, RemoteID: remoteID, RemoteURL: remoteURL, PayloadDigest: r.PayloadDigest}
}

func (c *GitHubClient) PublishAuthorized(ctx context.Context, req AuthorizedPublicationRequest) (AuthorizedPublicationReceipt, error) {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return AuthorizedPublicationReceipt{}, errors.New("github: authorized publisher is not configured")
	}
	change, err := req.validateEndpoint("github", c.BaseURL)
	if err != nil {
		return AuthorizedPublicationReceipt{}, err
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repoPath(req.Destination.RepositoryID), change)
	var comments []gitHubComment
	if err = c.getAll(ctx, path+"?per_page=100", &comments); err != nil {
		return AuthorizedPublicationReceipt{}, err
	}
	want := authorizedPublicationBody(req)
	for _, comment := range comments {
		if comment.Body == want {
			return authorizedReceipt("github", strconv.FormatInt(comment.ID, 10), comment.HTMLURL, req), nil
		}
	}
	for _, comment := range comments {
		if hasBotMarkerKind(comment.Body, "final") {
			var out gitHubComment
			if err = c.send(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repoPath(req.Destination.RepositoryID), comment.ID), map[string]string{"body": want}, &out); err != nil {
				return AuthorizedPublicationReceipt{}, err
			}
			if out.ID == 0 {
				out.ID, out.HTMLURL = comment.ID, comment.HTMLURL
			}
			return authorizedReceipt("github", strconv.FormatInt(out.ID, 10), out.HTMLURL, req), nil
		}
	}
	var out gitHubComment
	if err = c.send(ctx, http.MethodPost, path, map[string]string{"body": want}, &out); err != nil {
		return AuthorizedPublicationReceipt{}, err
	}
	if out.ID == 0 {
		return AuthorizedPublicationReceipt{}, errors.New("github: publication response missing comment identity")
	}
	return authorizedReceipt("github", strconv.FormatInt(out.ID, 10), out.HTMLURL, req), nil
}

func (c *GitHubClient) LookupAuthorized(ctx context.Context, req AuthorizedPublicationRequest) (*AuthorizedPublicationReceipt, error) {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return nil, errors.New("github: authorized publisher is not configured")
	}
	change, err := req.validateEndpoint("github", c.BaseURL)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repoPath(req.Destination.RepositoryID), change)
	var comments []gitHubComment
	if err = c.getAll(ctx, path+"?per_page=100", &comments); err != nil {
		return nil, err
	}
	var found *AuthorizedPublicationReceipt
	for _, comment := range comments {
		if comment.Body != authorizedPublicationBody(req) {
			continue
		}
		if found != nil {
			return nil, errors.New("github: duplicate exact publication markers")
		}
		receipt := authorizedReceipt("github", strconv.FormatInt(comment.ID, 10), comment.HTMLURL, req)
		found = &receipt
	}
	return found, nil
}

func (c *GitLabClient) PublishAuthorized(ctx context.Context, req AuthorizedPublicationRequest) (AuthorizedPublicationReceipt, error) {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return AuthorizedPublicationReceipt{}, errors.New("gitlab: authorized publisher is not configured")
	}
	change, err := req.validateEndpoint("gitlab", c.BaseURL)
	if err != nil {
		return AuthorizedPublicationReceipt{}, err
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/notes", url.PathEscape(req.Destination.RepositoryID), change)
	var notes []gitLabNote
	if err = c.getAll(ctx, path+"?per_page=100", &notes); err != nil {
		return AuthorizedPublicationReceipt{}, err
	}
	want := authorizedPublicationBody(req)
	for _, note := range notes {
		if note.Body == want {
			return authorizedReceipt("gitlab", strconv.Itoa(note.ID), note.URL, req), nil
		}
	}
	for _, note := range notes {
		if hasBotMarkerKind(note.Body, "final") {
			var out gitLabNote
			if err = c.send(ctx, http.MethodPut, fmt.Sprintf("%s/%d", path, note.ID), map[string]string{"body": want}, &out); err != nil {
				return AuthorizedPublicationReceipt{}, err
			}
			if out.ID == 0 {
				out.ID, out.URL = note.ID, note.URL
			}
			return authorizedReceipt("gitlab", strconv.Itoa(out.ID), out.URL, req), nil
		}
	}
	var out gitLabNote
	if err = c.send(ctx, http.MethodPost, path, map[string]string{"body": want}, &out); err != nil {
		return AuthorizedPublicationReceipt{}, err
	}
	if out.ID == 0 {
		return AuthorizedPublicationReceipt{}, errors.New("gitlab: publication response missing note identity")
	}
	return authorizedReceipt("gitlab", strconv.Itoa(out.ID), out.URL, req), nil
}

func (c *GitLabClient) LookupAuthorized(ctx context.Context, req AuthorizedPublicationRequest) (*AuthorizedPublicationReceipt, error) {
	if c == nil || c.BaseURL == "" || c.Token == "" {
		return nil, errors.New("gitlab: authorized publisher is not configured")
	}
	change, err := req.validateEndpoint("gitlab", c.BaseURL)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/api/v4/projects/%s/merge_requests/%d/notes", url.PathEscape(req.Destination.RepositoryID), change)
	var notes []gitLabNote
	if err = c.getAll(ctx, path+"?per_page=100", &notes); err != nil {
		return nil, err
	}
	var found *AuthorizedPublicationReceipt
	for _, note := range notes {
		if note.Body != authorizedPublicationBody(req) {
			continue
		}
		if found != nil {
			return nil, errors.New("gitlab: duplicate exact publication markers")
		}
		receipt := authorizedReceipt("gitlab", strconv.Itoa(note.ID), note.URL, req)
		found = &receipt
	}
	return found, nil
}
