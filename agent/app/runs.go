package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runs, err := s.pipeline.Jobs.List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if s.accountingRuntime != nil {
		projections, err := s.accountingRuntime.Authority.ListModelJobs(r.Context(), 128)
		if err != nil {
			http.Error(w, "durable runs unavailable", http.StatusServiceUnavailable)
			return
		}
		byID := make(map[string]pipeline.Run, len(runs)+len(projections))
		for _, run := range runs {
			byID[run.ID] = run
		}
		for index := range projections {
			run := projectedModelRun(&projections[index])
			byID[run.ID] = *run
		}
		runs = make([]pipeline.Run, 0, len(byID))
		for _, run := range byID {
			runs = append(runs, run)
		}
		sort.Slice(runs, func(i, j int) bool {
			if runs[i].UpdatedAt.Equal(runs[j].UpdatedAt) {
				return runs[i].ID < runs[j].ID
			}
			return runs[i].UpdatedAt.After(runs[j].UpdatedAt)
		})
	}
	out := make([]runDTO, 0, len(runs))
	for _, run := range runs {
		out = append(out, toRunDTO(run, false))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	run, err := s.lookupRun(r.Context(), id)
	if err != nil {
		if errors.Is(err, errDurableRunUnavailable) {
			http.Error(w, "durable run unavailable", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toRunDTO(*run, true))
}

var errDurableRunUnavailable = errors.New("durable run unavailable")

func (s *Server) lookupRun(ctx context.Context, id string) (*pipeline.Run, error) {
	if s.accountingRuntime != nil {
		projection, err := s.accountingRuntime.Authority.LookupModelJob(ctx, id)
		if err == nil {
			return projectedModelRun(projection), nil
		}
		if !errors.Is(err, accounting.ErrModelJobNotFound) {
			return nil, errors.Join(errDurableRunUnavailable, err)
		}
	}
	return s.pipeline.Jobs.Get(ctx, id)
}

func projectedModelRun(p *review.ModelJobProjection) *pipeline.Run {
	source := p.Source.Clone()
	status := pipeline.StatusFailed
	if p.Succeeded {
		status = pipeline.StatusDrafted
	}
	return &pipeline.Run{ID: p.RunID, Request: source.Request, Status: status, Error: p.Reason, Source: &source, Context: &review.Context{Source: source.Clone()}, DraftReport: source.Report.Draft, Findings: source.Findings, WebURL: source.Request.WebURL, UpdatedAt: p.CompletedAt}
}

type runDTO struct {
	ID               string                   `json:"id"`
	Provider         string                   `json:"provider"`
	ProjectID        string                   `json:"project_id"`
	ChangeID         string                   `json:"change_id"`
	MRIID            int                      `json:"mr_iid"`
	Title            string                   `json:"title,omitempty"`
	Status           pipeline.RunStatus       `json:"status"`
	Error            string                   `json:"error,omitempty"`
	WebURL           string                   `json:"web_url,omitempty"`
	UpdatedAt        time.Time                `json:"updated_at"`
	EventCount       int                      `json:"event_count"`
	Events           []pipeline.RunEvent      `json:"events,omitempty"`
	Findings         []review.Finding         `json:"findings,omitempty"`
	HumanCheck       []review.Finding         `json:"human_check,omitempty"`
	Notes            []review.Finding         `json:"notes,omitempty"`
	Questions        []review.Finding         `json:"questions,omitempty"`
	InlineComments   []review.InlineComment   `json:"inline_comments,omitempty"`
	ToolRequests     int                      `json:"tool_requests,omitempty"`
	ToolObservations int                      `json:"tool_observations,omitempty"`
	DraftReport      string                   `json:"draft_report,omitempty"`
	FinalReport      string                   `json:"final_report,omitempty"`
	HILApproved      bool                     `json:"hil_approved"`
	Snapshot         *review.SnapshotIdentity `json:"snapshot,omitempty"`
	Policy           *review.PolicyProjection `json:"policy,omitempty"`
}

func toRunDTO(run pipeline.Run, includeDetails bool) runDTO {
	dto := runDTO{
		ID:          run.ID,
		Provider:    run.Request.Provider,
		ProjectID:   run.Request.ProjectID,
		ChangeID:    run.Request.ChangeID,
		MRIID:       run.Request.MRIID,
		Title:       run.Request.Title,
		Status:      run.Status,
		Error:       run.Error,
		WebURL:      run.WebURL,
		UpdatedAt:   run.UpdatedAt,
		EventCount:  len(run.Events),
		HILApproved: run.HILApproved,
	}
	if includeDetails {
		dto.Events = append([]pipeline.RunEvent(nil), run.Events...)
		dto.Findings = run.Findings
		if run.Source != nil {
			if run.Source.Attestation != nil {
				snapshot := run.Source.Attestation.Snapshot
				dto.Snapshot = &snapshot
			}
			if run.Source.Policy.Mode != "" {
				policyProjection := run.Source.Policy
				dto.Policy = &policyProjection
			}
			dto.HumanCheck = append([]review.Finding(nil), run.Source.HumanCheck...)
			dto.Notes = append([]review.Finding(nil), run.Source.Notes...)
			dto.Questions = append([]review.Finding(nil), run.Source.Questions...)
			dto.InlineComments = append([]review.InlineComment(nil), run.Source.InlineComments...)
			dto.ToolRequests = len(run.Source.ToolRequests)
			dto.ToolObservations = len(run.Source.ToolObservations)
		}
		dto.DraftReport = run.DraftReport
		dto.FinalReport = run.FinalReport
	}
	return dto
}
