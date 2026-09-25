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

	"github.com/Y4NN777/7review/agent/review"
)

type HeadroomReducer struct {
	BaseURL    string
	Timeout    time.Duration
	HTTPClient *http.Client
}

func NewHeadroomReducer(baseURL string, timeout time.Duration) *HeadroomReducer {
	return &HeadroomReducer{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Timeout:    timeout,
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

func (r *HeadroomReducer) Check(ctx context.Context) error {
	if r == nil || r.BaseURL == "" {
		return fmt.Errorf("headroom: missing base URL")
	}
	return r.send(ctx, http.MethodGet, "/health", nil, nil)
}

func (r *HeadroomReducer) Reduce(ctx context.Context, rc *review.Context) error {
	if r == nil || r.BaseURL == "" {
		return fmt.Errorf("headroom: missing base URL")
	}
	var out headroomReduceResponse
	err := r.send(ctx, http.MethodPost, "/reduce", headroomReduceRequest{
		Request:        rc.Request,
		SkillSections:  rc.SkillSections,
		CorpusSections: rc.CorpusSections,
		Memory:         rc.Source.Memory,
		Diff:           rc.Diff,
	}, &out)
	if err != nil {
		return err
	}
	if out.SkillSections != nil {
		rc.Source.SkillSections = out.SkillSections
	}
	if out.CorpusSections != nil {
		rc.Source.CorpusSections = out.CorpusSections
	}
	if out.Memory != nil {
		rc.Source.Memory = *out.Memory
	}
	if out.Diff != nil {
		rc.Diff = out.Diff
	}
	for _, warning := range out.Warnings {
		rc.AddWarning("headroom: " + warning)
	}
	return nil
}

func (r *HeadroomReducer) send(ctx context.Context, method, path string, in any, out any) error {
	callCtx, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()

	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("headroom: marshal %s: %w", path, err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(callCtx, method, r.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("headroom: request %s: %w", path, err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return fmt.Errorf("headroom: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("headroom: %s %s: %s: %s", method, path, resp.Status, readToolErrorBody(resp.Body))
	}
	return decodeToolJSON("headroom", method, path, resp.Body, out)
}

func (r *HeadroomReducer) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return 5 * time.Second
}

func (r *HeadroomReducer) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: r.timeout()}
}

type headroomReduceRequest struct {
	Request        review.Request         `json:"request"`
	SkillSections  []review.Section       `json:"skill_sections"`
	CorpusSections []review.Section       `json:"corpus_sections"`
	Memory         review.MemoryRecall    `json:"memory"`
	Diff           *review.StructuredDiff `json:"diff,omitempty"`
}

type headroomReduceResponse struct {
	SkillSections  []review.Section       `json:"skill_sections,omitempty"`
	CorpusSections []review.Section       `json:"corpus_sections,omitempty"`
	Memory         *review.MemoryRecall   `json:"memory,omitempty"`
	Diff           *review.StructuredDiff `json:"diff,omitempty"`
	Warnings       []string               `json:"warnings,omitempty"`
}
