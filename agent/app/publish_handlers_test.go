package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestHandlePublishFinalEnqueuesRunPublish(t *testing.T) {
	called := make(chan struct{}, 1)
	store := pipeline.NewMemoryRunStore()
	reqRun := review.Request{Provider: "gitlab", ProjectID: "p", MRIID: 7, ChangeID: "7"}
	run, err := store.Start(context.Background(), reqRun)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(reqRun)
	rc.HILApproved = true
	rc.Source.Report.Final = "old final"
	rc.Source.SCM = &review.SCMContext{Provider: "gitlab", ProjectID: "p", MRIID: 7, ChangeID: "7"}
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), run.ID, pipeline.StatusDrafted, nil); err != nil {
		t.Fatal(err)
	}
	publisher := &fakeAppPublisher{}
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs:         store,
			SCMPublisher: publisher,
			Memory:       fakeMemory{},
		},
		work: make(chan workItem, 1),
	}
	s.pipeline.SCM = fakeAppSCM{}
	req := httptest.NewRequest(http.MethodPost, "/publish/final?run=p!7", strings.NewReader("final report"))
	rec := httptest.NewRecorder()

	s.handlePublishFinal(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	item := <-s.work
	go func() {
		_ = item.run(context.Background())
		called <- struct{}{}
	}()
	<-called
	if publisher.finalReport != "final report" {
		t.Fatalf("final report was not published: %#v", publisher)
	}
}

func TestHandlePublishFinalRejectsOversizedReport(t *testing.T) {
	s := &Server{
		pipeline: &pipeline.Pipeline{Jobs: pipeline.NewMemoryRunStore()},
		work:     make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/publish/final?run=p!7", strings.NewReader(strings.Repeat("x", int(reportMaxBodyBytes)+1)))
	rec := httptest.NewRecorder()

	s.handlePublishFinal(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 0 {
		t.Fatal("oversized final report should not enqueue work")
	}
}

func TestHandleApproveAcceptsRunID(t *testing.T) {
	called := make(chan struct{}, 1)
	store := pipeline.NewMemoryRunStore()
	reqRun := review.Request{Provider: "github", ProjectID: "owner/repo", MRIID: 7, ChangeID: "7"}
	run, err := store.Start(context.Background(), reqRun)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(reqRun)
	rc.Source.SCM = &review.SCMContext{Provider: "github", Repository: "owner/repo", ProjectID: "owner/repo", MRIID: 7, ChangeID: "7"}
	rc.Source.Report.Draft = "draft"
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), run.ID, pipeline.StatusDrafted, nil); err != nil {
		t.Fatal(err)
	}
	publisher := &fakeAppPublisher{}
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs:         store,
			SCM:          fakeAppSCM{},
			SCMPublisher: publisher,
			Memory:       fakeMemory{},
		},
		work: make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/approve?run=owner%2Frepo%217", strings.NewReader("approved report"))
	rec := httptest.NewRecorder()

	s.handleApprove(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	item := <-s.work
	go func() {
		_ = item.run(context.Background())
		called <- struct{}{}
	}()
	<-called
	if publisher.finalReport != "approved report" {
		t.Fatalf("approved report was not published: %#v", publisher)
	}
	updated, err := store.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.HILApproved || updated.Status != pipeline.StatusFinalized {
		t.Fatalf("run was not approved through run id: %#v", updated)
	}
}

func TestHandleApproveDoesNotPublishUndraftedRun(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	reqRun := review.Request{Provider: "github", ProjectID: "owner/repo", MRIID: 7, ChangeID: "7"}
	run, err := store.Start(context.Background(), reqRun)
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(reqRun)
	rc.Source.SCM = &review.SCMContext{Provider: "github", Repository: "owner/repo", ProjectID: "owner/repo", MRIID: 7, ChangeID: "7"}
	if err := store.SaveContext(context.Background(), run.ID, rc); err != nil {
		t.Fatal(err)
	}
	publisher := &fakeAppPublisher{}
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs:         store,
			SCM:          fakeAppSCM{},
			SCMPublisher: publisher,
			Memory:       fakeMemory{},
		},
		work: make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/approve?run=owner%2Frepo%217", strings.NewReader("approved report"))
	rec := httptest.NewRecorder()

	s.handleApprove(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected queued approval, got %d: %s", rec.Code, rec.Body.String())
	}
	item := <-s.work
	if err := item.run(context.Background()); err == nil || !strings.Contains(err.Error(), "draft report required") {
		t.Fatalf("expected draft requirement error, got %v", err)
	}
	if publisher.finalReport != "" {
		t.Fatalf("publisher should not run for undrafted approval: %#v", publisher)
	}
}

func TestHandleApproveRejectsOversizedReport(t *testing.T) {
	s := &Server{
		pipeline: &pipeline.Pipeline{Jobs: pipeline.NewMemoryRunStore()},
		work:     make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/approve?run=owner%2Frepo%217", strings.NewReader(strings.Repeat("x", int(reportMaxBodyBytes)+1)))
	rec := httptest.NewRecorder()

	s.handleApprove(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 0 {
		t.Fatal("oversized approval report should not enqueue work")
	}
}
