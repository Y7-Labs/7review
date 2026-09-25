package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

// SCM enriches webhook events with merge/pull request API context.
type SCM interface {
	Enrich(context.Context, review.Request) (*review.SCMContext, error)
}

var ErrRepositoryFileNotFound = errors.New("repository file not found")

const repositoryFileReadLimit = 2 << 20

type RepositoryFileReader interface {
	ReadRepositoryFile(context.Context, string, string, string) ([]byte, error)
}

// Publisher writes review reports back to the source-control platform.
type Publisher interface {
	PublishDraft(context.Context, *review.SCMContext, string) error
	PublishFinal(context.Context, *review.SCMContext, string) error
}

// InlinePublisher writes one validated finding as an inline draft comment.
type InlinePublisher interface {
	PublishInlineDraft(context.Context, *review.SCMContext, review.InlineComment) (review.InlineComment, error)
}

type ProviderRouter struct {
	SCM        map[string]SCM
	Publishers map[string]Publisher
}

func (r ProviderRouter) ReadRepositoryFile(ctx context.Context, provider, repositoryID, revision, path string) ([]byte, error) {
	tool, ok := r.SCM[strings.ToLower(provider)]
	if !ok || tool == nil {
		return nil, fmt.Errorf("tools: no SCM configured for provider %q", provider)
	}
	reader, ok := tool.(RepositoryFileReader)
	if !ok {
		return nil, fmt.Errorf("tools: provider %q cannot read repository files", provider)
	}
	return reader.ReadRepositoryFile(ctx, repositoryID, revision, path)
}

func (r ProviderRouter) Enrich(ctx context.Context, req review.Request) (*review.SCMContext, error) {
	tool, ok := r.SCM[strings.ToLower(req.Provider)]
	if !ok || tool == nil {
		return nil, fmt.Errorf("tools: no SCM configured for provider %q", req.Provider)
	}
	return tool.Enrich(ctx, req)
}

func (r ProviderRouter) PublishDraft(ctx context.Context, source *review.SCMContext, report string) error {
	tool, ok := r.Publishers[strings.ToLower(source.Provider)]
	if !ok || tool == nil {
		return nil
	}
	return tool.PublishDraft(ctx, source, report)
}

func (r ProviderRouter) PublishFinal(ctx context.Context, source *review.SCMContext, report string) error {
	tool, ok := r.Publishers[strings.ToLower(source.Provider)]
	if !ok || tool == nil {
		return nil
	}
	return tool.PublishFinal(ctx, source, report)
}

func (r ProviderRouter) PublishInlineDraft(ctx context.Context, source *review.SCMContext, comment review.InlineComment) (review.InlineComment, error) {
	if source == nil {
		comment.Status = "skipped"
		comment.Reason = "SCM context is unavailable"
		return comment, nil
	}
	tool, ok := r.Publishers[strings.ToLower(source.Provider)]
	if !ok || tool == nil {
		comment.Status = "skipped"
		comment.Reason = "provider does not support inline draft comments"
		return comment, nil
	}
	inline, ok := tool.(InlinePublisher)
	if !ok {
		comment.Status = "skipped"
		comment.Reason = "provider does not support inline draft comments"
		return comment, nil
	}
	return inline.PublishInlineDraft(ctx, source, comment)
}

// NoopSCM is a safe development default that uses request fields only.
type NoopSCM struct{}

func (NoopSCM) Enrich(_ context.Context, req review.Request) (*review.SCMContext, error) {
	files := make([]review.ChangedFile, 0, len(req.ChangedPaths))
	for _, path := range req.ChangedPaths {
		files = append(files, review.ChangedFile{NewPath: path, Status: "modified"})
	}
	return &review.SCMContext{
		Provider:    req.Provider,
		ProjectID:   req.ProjectID,
		Repository:  req.Repository,
		ChangeID:    req.ChangeID,
		MRIID:       req.MRIID,
		Title:       req.Title,
		Description: req.Description,
		Author:      req.Author,
		WebURL:      req.WebURL,
		Labels:      req.Labels,
		DiffRefs: review.DiffRefs{
			BaseSHA: req.TargetSHA,
			HeadSHA: req.SourceSHA,
		},
		Files: files,
	}, nil
}

// NoopPublisher accepts reports without publishing them externally.
type NoopPublisher struct{}

func (NoopPublisher) PublishDraft(context.Context, *review.SCMContext, string) error {
	return nil
}

func (NoopPublisher) PublishFinal(context.Context, *review.SCMContext, string) error {
	return nil
}

func (NoopPublisher) PublishInlineDraft(_ context.Context, _ *review.SCMContext, comment review.InlineComment) (review.InlineComment, error) {
	comment.Status = "skipped"
	comment.Reason = "publisher is not configured"
	return comment, nil
}
