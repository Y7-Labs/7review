package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/review"
)

func TestMemPalaceStore_RecallUsesQueryEmbeddingWhenEnabled(t *testing.T) {
	store := NewMemPalaceStore("http://mempalace", time.Second)
	store.EmbeddingModel = "embed-model"
	store.Embedder = fakeEmbedder{Vector: []float64{0.5, 0.25}}
	store.EmbedQueries = true
	store.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/recall" {
			return response(http.StatusNotFound, ""), nil
		}
		var payload memPalaceRecallRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Query == "" || len(payload.QueryEmbedding) != 2 || payload.QueryEmbedding[0] != 0.5 {
			t.Fatalf("unexpected recall payload: %#v", payload)
		}
		return jsonResponse(review.MemoryRecall{Conventions: []string{"conv"}, Decisions: []string{"decision"}}), nil
	})}

	recall, err := store.Recall(context.Background(), review.Request{Title: "auth change", ChangedPaths: []string{"auth.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if recall.Conventions[0] != "conv" || recall.Decisions[0] != "decision" {
		t.Fatalf("unexpected recall: %#v", recall)
	}
}

func TestMemPalaceStore_RecallOmitsQueryEmbeddingByDefault(t *testing.T) {
	store := NewMemPalaceStore("http://mempalace", time.Second)
	store.EmbeddingModel = "embed-model"
	store.Embedder = fakeEmbedder{Vector: []float64{0.5, 0.25}}
	store.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload memPalaceRecallRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.QueryEmbedding) != 0 {
			t.Fatalf("expected no query embedding by default, got %#v", payload.QueryEmbedding)
		}
		return jsonResponse(review.MemoryRecall{}), nil
	})}

	if _, err := store.Recall(context.Background(), review.Request{Title: "auth change"}); err != nil {
		t.Fatal(err)
	}
}

func TestMemPalaceStore_WriteEmbedsVectorsWhenEnabled(t *testing.T) {
	var wrote bool
	store := NewMemPalaceStore("http://mempalace", time.Second)
	store.EmbeddingModel = "embed-model"
	store.Embedder = fakeEmbedder{Vector: []float64{0.5, 0.25}}
	store.EmbedWrites = true
	store.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"Embedding":[0.5,0.25]`) {
			t.Fatalf("write payload missing embedding: %s", string(body))
		}
		wrote = true
		return response(http.StatusNoContent, ""), nil
	})}

	if err := store.Write(context.Background(), review.UpdateProposal{Vectors: []review.Vector{{ID: "v1", Text: "final"}}}); err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("expected write call")
	}
}

func TestMemPalaceStore_ProposeUpdateRequiresApproval(t *testing.T) {
	store := NewMemPalaceStore("http://mempalace", time.Second)
	_, err := store.ProposeUpdate(context.Background(), review.NewContext(review.Request{}))
	if err == nil {
		t.Fatal("expected approval error")
	}
}

func TestMemPalaceStore_ProposeUpdateUsesFinalOnly(t *testing.T) {
	store := NewMemPalaceStore("http://mempalace", time.Second)
	rc := review.NewContext(review.Request{})
	rc.HILApproved = true
	rc.Source.Report.Final = "final report"
	rc.Findings = []review.Finding{{ID: "F1", Title: "accepted"}}
	rc.HILAddedNotes = []string{"human note"}

	proposal, err := store.ProposeUpdate(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(proposal.Conventions) != 2 || proposal.Decisions[0] != "human note" {
		t.Fatalf("unexpected proposal: %#v", proposal)
	}
	if len(proposal.Vectors) != 3 {
		t.Fatalf("expected vectors for conventions and decisions, got %#v", proposal.Vectors)
	}
}

type fakeEmbedder struct {
	Vector []float64
	Err    error
}

func (f fakeEmbedder) Embed(context.Context, llm.EmbeddingRequest) ([]float64, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return append([]float64(nil), f.Vector...), nil
}
