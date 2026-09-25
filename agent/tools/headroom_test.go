package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func TestHeadroomReducer_ReduceUpdatesSelectedContext(t *testing.T) {
	reducer := NewHeadroomReducer("http://headroom", time.Second)
	reducer.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/reduce" {
			return response(http.StatusNotFound, ""), nil
		}
		return jsonResponse(headroomReduceResponse{
			SkillSections:  []review.Section{{Path: "skill", Title: "Reduced Skill", Content: "short", Kind: review.KindRules}},
			CorpusSections: []review.Section{{Path: "doc", Title: "Reduced Doc", Content: "short", Kind: review.KindContract}},
			Memory:         &review.MemoryRecall{Conventions: []string{"c1"}, Decisions: []string{"d1"}},
			Diff:           &review.StructuredDiff{Files: []review.FileDiff{{Path: "main.go", Patch: "@@", TokenCount: 1}}},
			Warnings:       []string{"trimmed context"},
		}), nil
	})}

	rc := review.NewContext(review.Request{})
	rc.SkillSections = []review.Section{{Path: "skill", Title: "Original", Content: "long", Kind: review.KindRules}}
	rc.CorpusSections = []review.Section{{Path: "doc", Title: "Original", Content: "long", Kind: review.KindContract}}
	rc.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "main.go", Patch: "long", TokenCount: 100}}}

	if err := reducer.Reduce(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	if rc.SkillSections[0].Title != "Reduced Skill" || rc.CorpusSections[0].Title != "Reduced Doc" {
		t.Fatalf("context was not reduced: %#v %#v", rc.SkillSections, rc.CorpusSections)
	}
	if len(rc.Source.Memory.Conventions) != 1 || rc.Source.Memory.Conventions[0] != "c1" || len(rc.Source.Memory.Decisions) != 1 || rc.Source.Memory.Decisions[0] != "d1" {
		t.Fatalf("memory not updated: %#v", rc.Source.Memory)
	}
	if len(rc.Run.Warnings) != 1 {
		t.Fatalf("expected warning, got %#v", rc.Run.Warnings)
	}
}

func TestHeadroomReducer_CheckFailure(t *testing.T) {
	reducer := NewHeadroomReducer("http://headroom", time.Second)
	reducer.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(http.StatusServiceUnavailable, "down"), nil
	})}
	if err := reducer.Check(context.Background()); err == nil {
		t.Fatal("expected check failure")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(v any) *http.Response {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(v)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(&buf),
		Header:     make(http.Header),
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
}
