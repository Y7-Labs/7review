package app

import (
	"encoding/json"
	"net/http"
	"time"

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
	run, err := s.pipeline.Jobs.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toRunDTO(*run, true))
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
