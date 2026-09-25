package review

import (
	"fmt"
	"sort"
	"sync"
	"testing"
)

func TestNewContextInitializesSourceAndRunMetadata(t *testing.T) {
	req := Request{
		Provider:   "github",
		ProjectID:  "owner/repo",
		ChangeID:   "7",
		MRIID:      7,
		Title:      "Review agent",
		Repository: "owner/repo",
	}
	rc := NewContext(req)

	if rc.Request.Provider != req.Provider || rc.Request.ProjectID != req.ProjectID || rc.Request.ChangeID != req.ChangeID {
		t.Fatalf("request was not initialized consistently: %#v", rc)
	}
	if rc.Source.Request.Provider != req.Provider || rc.Source.Request.ProjectID != req.ProjectID || rc.Source.Request.ChangeID != req.ChangeID {
		t.Fatalf("source request was not initialized consistently: %#v", rc.Source.Request)
	}
	rc.Request.Title = "canonical mutation"
	if rc.Source.Request.Title != "canonical mutation" {
		t.Fatalf("context request is not backed by source request: %#v", rc.Source.Request)
	}
	if rc.Source.Run.StepProviders == nil {
		t.Fatalf("step providers not initialized: %#v", rc)
	}
	if rc.Source.Run.StartedAt.IsZero() {
		t.Fatal("run start time was not initialized")
	}
}

func TestContextConcurrentFindingsAreSnapshotCopies(t *testing.T) {
	rc := NewContext(Request{ProjectID: "p", ChangeID: "1"})
	const total = 32
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rc.AddFindings(fmt.Sprintf("finding-%02d", i))
		}(i)
	}
	wg.Wait()

	got := rc.AllFindings()
	if len(got) != total {
		t.Fatalf("expected %d findings, got %d: %#v", total, len(got), got)
	}
	got[0] = "mutated"
	again := rc.AllFindings()
	sort.Strings(again)
	if again[0] == "mutated" {
		t.Fatalf("AllFindings leaked internal slice: %#v", again)
	}
}

func TestRecordProviderAndWarningsUpdateSourceRunMetadata(t *testing.T) {
	rc := NewContext(Request{ProjectID: "p", ChangeID: "1"})

	rc.RecordProvider("review", "openai/gpt-test")
	rc.AddWarning("headroom trimmed context")

	if rc.Source.Run.StepProviders["review"] != "openai/gpt-test" {
		t.Fatalf("source provider map not updated: %#v", rc.Source.Run.StepProviders)
	}
	if len(rc.Source.Run.Warnings) != 1 || rc.Source.Run.Warnings[0] != "headroom trimmed context" {
		t.Fatalf("source warnings not updated: %#v", rc.Source.Run.Warnings)
	}
}

func TestChangedPathsUsesCanonicalSourceDiff(t *testing.T) {
	rc := NewContext(Request{ProjectID: "p", ChangeID: "1"})
	rc.Diff = &StructuredDiff{Files: []FileDiff{
		{Path: "agent/app/server.go"},
		{Path: "cmd/7review/main.go"},
	}}
	if rc.Source.Diff != rc.Diff {
		t.Fatal("context diff is not backed by source diff")
	}

	got := rc.ChangedPaths()
	want := []string{"agent/app/server.go", "cmd/7review/main.go"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unexpected paths: got %#v want %#v", got, want)
	}
	got[0] = "mutated"
	if rc.Source.Diff.Files[0].Path == "mutated" {
		t.Fatalf("ChangedPaths leaked internal diff slice")
	}
}
