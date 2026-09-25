package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/tools"
)

type publishStatusDTO struct {
	Run             string                 `json:"run"`
	Status          pipeline.RunStatus     `json:"status"`
	WebURL          string                 `json:"web_url,omitempty"`
	HILApproved     bool                   `json:"hil_approved"`
	HasDraftReport  bool                   `json:"has_draft_report"`
	HasFinalReport  bool                   `json:"has_final_report"`
	DraftBytes      int                    `json:"draft_bytes"`
	FinalBytes      int                    `json:"final_bytes"`
	Error           string                 `json:"error,omitempty"`
	Provider        string                 `json:"provider,omitempty"`
	ProjectID       string                 `json:"project_id,omitempty"`
	ChangeID        string                 `json:"change_id,omitempty"`
	SCMDiscussions  int                    `json:"scm_discussions"`
	SCMChecks       int                    `json:"scm_checks"`
	SCMApprovals    int                    `json:"scm_approvals"`
	InlineComments  []review.InlineComment `json:"inline_comments,omitempty"`
	UpdatedAtUnixMS int64                  `json:"updated_at_unix_ms"`
}

type memoryProposalStatusDTO struct {
	Run        string                `json:"run"`
	Approved   bool                  `json:"approved"`
	Proposal   review.UpdateProposal `json:"proposal"`
	FinalBytes int                   `json:"final_bytes"`
}

func (r appToolRunner) PublishStatus(ctx context.Context, id string) (any, error) {
	run, err := r.server.pipeline.Jobs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	source := sourceForRun(run)
	return publishStatusDTO{
		Run:             run.ID,
		Status:          run.Status,
		WebURL:          run.WebURL,
		HILApproved:     run.HILApproved,
		HasDraftReport:  strings.TrimSpace(run.DraftReport) != "",
		HasFinalReport:  strings.TrimSpace(run.FinalReport) != "",
		DraftBytes:      len(run.DraftReport),
		FinalBytes:      len(run.FinalReport),
		Error:           run.Error,
		Provider:        sourceProvider(source, run),
		ProjectID:       sourceProjectID(source, run),
		ChangeID:        sourceChangeID(source, run),
		SCMDiscussions:  len(sourceSCM(source).Discussions),
		SCMChecks:       len(sourceSCM(source).Checks),
		SCMApprovals:    len(sourceSCM(source).Approvals),
		InlineComments:  append([]review.InlineComment(nil), source.InlineComments...),
		UpdatedAtUnixMS: run.UpdatedAt.UnixMilli(),
	}, nil
}

func (r appToolRunner) MemoryProposal(ctx context.Context, id string) (any, error) {
	if r.server == nil || r.server.pipeline == nil || r.server.pipeline.Memory == nil {
		return nil, fmt.Errorf("memory store is not configured")
	}
	run, err := r.server.pipeline.Jobs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	rc := contextForRunPreview(run)
	proposal, err := r.server.pipeline.Memory.ProposeUpdate(ctx, rc)
	if err != nil {
		return nil, err
	}
	return memoryProposalStatusDTO{
		Run:        run.ID,
		Approved:   rc.HILApproved,
		Proposal:   proposal,
		FinalBytes: len(rc.Source.Report.Final),
	}, nil
}

func sourceSCM(source review.Source) *review.SCMContext {
	if source.SCM != nil {
		return source.SCM
	}
	return &review.SCMContext{}
}

func sourceProvider(source review.Source, run *pipeline.Run) string {
	if source.SCM != nil && source.SCM.Provider != "" {
		return source.SCM.Provider
	}
	if run != nil {
		return run.Request.Provider
	}
	return ""
}

func sourceProjectID(source review.Source, run *pipeline.Run) string {
	if source.SCM != nil && source.SCM.ProjectID != "" {
		return source.SCM.ProjectID
	}
	if run != nil {
		return run.Request.ProjectID
	}
	return ""
}

func sourceChangeID(source review.Source, run *pipeline.Run) string {
	if source.SCM != nil && source.SCM.ChangeID != "" {
		return source.SCM.ChangeID
	}
	if run != nil {
		return run.Request.ChangeID
	}
	return ""
}

func contextForRunPreview(run *pipeline.Run) *review.Context {
	if run == nil {
		return review.NewContext(review.Request{})
	}
	if run.Context != nil {
		return run.Context
	}
	rc := review.NewContext(run.Request)
	if run.Source != nil {
		rc.Source = *run.Source
	}
	rc.Request = run.Request
	rc.Source.Report.Draft = run.DraftReport
	rc.Source.Report.Final = run.FinalReport
	rc.HILApproved = run.HILApproved
	rc.Source.Findings = append([]review.Finding(nil), run.Findings...)
	if rc.Request.WebURL == "" {
		rc.Request.WebURL = run.WebURL
	}
	return rc
}

var _ tools.Observatory = appToolRunner{}
