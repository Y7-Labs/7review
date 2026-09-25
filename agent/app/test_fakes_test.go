package app

import (
	"context"
	"errors"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

type fakeReducer struct {
	err error
}

func (f fakeReducer) Reduce(context.Context, *review.Context) error {
	return f.err
}

func (f fakeReducer) Check(context.Context) error {
	return f.err
}

type fakeMemory struct{}

func (fakeMemory) Recall(context.Context, review.Request) (pipeline.Recall, error) {
	return pipeline.Recall{}, nil
}

func (fakeMemory) ProposeUpdate(context.Context, *review.Context) (pipeline.UpdateProposal, error) {
	return pipeline.UpdateProposal{}, nil
}

func (fakeMemory) Write(context.Context, pipeline.UpdateProposal) error {
	return nil
}

func (fakeMemory) Check(context.Context) error {
	return nil
}

type proposalMemory struct{}

func (proposalMemory) Recall(context.Context, review.Request) (pipeline.Recall, error) {
	return pipeline.Recall{}, nil
}

func (proposalMemory) ProposeUpdate(_ context.Context, rc *review.Context) (pipeline.UpdateProposal, error) {
	if rc == nil || !rc.HILApproved {
		return pipeline.UpdateProposal{}, errors.New("approval required")
	}
	return pipeline.UpdateProposal{Conventions: []string{rc.Source.Report.Final}}, nil
}

func (proposalMemory) Write(context.Context, pipeline.UpdateProposal) error {
	return nil
}

func (proposalMemory) Check(context.Context) error {
	return nil
}

type streamingProvider struct{}

func (streamingProvider) Name() string { return "fake" }

func (streamingProvider) Complete(context.Context, orchestrator.LLMRequest) (string, error) {
	return "complete", nil
}

func (streamingProvider) Stream(_ context.Context, _ orchestrator.LLMRequest, emit orchestrator.StreamHandler) error {
	if err := emit("stream "); err != nil {
		return err
	}
	return emit("reply")
}

type staticResponseProvider struct {
	response string
}

func (p staticResponseProvider) Name() string { return "fake" }

func (p staticResponseProvider) Complete(context.Context, orchestrator.LLMRequest) (string, error) {
	return p.response, nil
}

type fakeAppSCM struct{}

func (fakeAppSCM) Enrich(context.Context, review.Request) (*review.SCMContext, error) {
	return &review.SCMContext{Provider: "gitlab", ProjectID: "p", MRIID: 7, ChangeID: "7"}, nil
}

type fakeAppPublisher struct {
	finalReport string
}

func (fakeAppPublisher) PublishDraft(context.Context, *review.SCMContext, string) error {
	return nil
}

func (p *fakeAppPublisher) PublishFinal(_ context.Context, _ *review.SCMContext, report string) error {
	p.finalReport = report
	return nil
}
