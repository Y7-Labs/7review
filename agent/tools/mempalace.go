package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/review"
)

type MemPalaceStore struct {
	BaseURL        string
	Timeout        time.Duration
	HTTPClient     *http.Client
	Embedder       llm.Embedder
	EmbeddingModel string
	EmbedQueries   bool
	EmbedWrites    bool
}

func NewMemPalaceStore(baseURL string, timeout time.Duration) *MemPalaceStore {
	return &MemPalaceStore{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Timeout:    timeout,
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (s *MemPalaceStore) Check(ctx context.Context) error {
	if s == nil || s.BaseURL == "" {
		return fmt.Errorf("mempalace: missing base URL")
	}
	return s.send(ctx, http.MethodGet, "/health", nil, nil)
}

func (s *MemPalaceStore) Recall(ctx context.Context, req review.Request) (review.MemoryRecall, error) {
	if s == nil || s.BaseURL == "" {
		return review.MemoryRecall{}, fmt.Errorf("mempalace: missing base URL")
	}
	query := memoryQuery(req)
	embedding, err := s.embed(ctx, query)
	if err != nil {
		return review.MemoryRecall{}, err
	}
	var out review.MemoryRecall
	err = s.send(ctx, http.MethodPost, "/recall", memPalaceRecallRequest{
		Request:        req,
		Query:          query,
		QueryEmbedding: embedding,
	}, &out)
	if err != nil {
		return review.MemoryRecall{}, err
	}
	return out, nil
}

func (s *MemPalaceStore) ProposeUpdate(_ context.Context, rc *review.Context) (review.UpdateProposal, error) {
	if rc == nil || !rc.HILApproved {
		return review.UpdateProposal{}, fmt.Errorf("mempalace: final approval required before memory proposal")
	}
	var conventions []string
	for _, finding := range rc.Findings {
		if finding.ID == "" {
			continue
		}
		conventions = append(conventions, fmt.Sprintf("%s: %s", finding.ID, finding.Title))
	}
	if rc.Source.Report.Final != "" {
		conventions = append(conventions, rc.Source.Report.Final)
	}
	var vectors []review.Vector
	for i, convention := range conventions {
		vectors = append(vectors, review.Vector{
			ID:   fmt.Sprintf("convention-%d", i+1),
			Text: convention,
		})
	}
	for i, decision := range rc.HILAddedNotes {
		vectors = append(vectors, review.Vector{
			ID:   fmt.Sprintf("decision-%d", i+1),
			Text: decision,
		})
	}
	return review.UpdateProposal{
		Conventions: conventions,
		Decisions:   append([]string(nil), rc.HILAddedNotes...),
		Vectors:     vectors,
	}, nil
}

func (s *MemPalaceStore) Write(ctx context.Context, proposal review.UpdateProposal) error {
	if s == nil || s.BaseURL == "" {
		return fmt.Errorf("mempalace: missing base URL")
	}
	enriched, err := s.embedProposal(ctx, proposal)
	if err != nil {
		return err
	}
	return s.send(ctx, http.MethodPost, "/write", enriched, nil)
}

func (s *MemPalaceStore) embed(ctx context.Context, text string) ([]float64, error) {
	if s == nil || !s.EmbedQueries || s.Embedder == nil || strings.TrimSpace(s.EmbeddingModel) == "" || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	embedding, err := s.Embedder.Embed(ctx, llm.EmbeddingRequest{Model: s.EmbeddingModel, Input: text})
	if err != nil {
		return nil, fmt.Errorf("mempalace: embed query with %s: %w", s.EmbeddingModel, err)
	}
	return embedding, nil
}

func (s *MemPalaceStore) embedProposal(ctx context.Context, proposal review.UpdateProposal) (review.UpdateProposal, error) {
	if s == nil || !s.EmbedWrites || s.Embedder == nil || strings.TrimSpace(s.EmbeddingModel) == "" {
		return proposal, nil
	}
	out := proposal
	out.Vectors = append([]review.Vector(nil), proposal.Vectors...)
	for i := range out.Vectors {
		if len(out.Vectors[i].Embedding) > 0 || strings.TrimSpace(out.Vectors[i].Text) == "" {
			continue
		}
		embedding, err := s.Embedder.Embed(ctx, llm.EmbeddingRequest{Model: s.EmbeddingModel, Input: out.Vectors[i].Text})
		if err != nil {
			return review.UpdateProposal{}, fmt.Errorf("mempalace: embed vector %s with %s: %w", out.Vectors[i].ID, s.EmbeddingModel, err)
		}
		out.Vectors[i].Embedding = embedding
	}
	return out, nil
}

func (s *MemPalaceStore) send(ctx context.Context, method, path string, in any, out any) error {
	callCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("mempalace: marshal %s: %w", path, err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(callCtx, method, s.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("mempalace: request %s: %w", path, err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("mempalace: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("mempalace: %s %s: %s: %s", method, path, resp.Status, readToolErrorBody(resp.Body))
	}
	return decodeToolJSON("mempalace", method, path, resp.Body, out)
}

func (s *MemPalaceStore) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 5 * time.Second
}

func (s *MemPalaceStore) client() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return &http.Client{Timeout: s.timeout()}
}

func memoryQuery(req review.Request) string {
	parts := []string{req.Provider, req.ProjectID, req.Repository, req.ChangeID, req.Title, req.Description}
	parts = append(parts, req.Labels...)
	parts = append(parts, req.ChangedPaths...)
	return strings.Join(parts, " ")
}

type memPalaceRecallRequest struct {
	Request        review.Request `json:"request"`
	Query          string         `json:"query"`
	QueryEmbedding []float64      `json:"query_embedding,omitempty"`
}
