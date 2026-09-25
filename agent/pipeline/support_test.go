package pipeline

import (
	"context"
	"testing"

	"github.com/Y4NN777/7review/agent/review"
)

func TestMemoryRunStoreDetachesCanonicalSourceSnapshots(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRunStore()
	run, err := store.Start(ctx, review.Request{ProjectID: "p", ChangeID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	rc := review.NewContext(review.Request{ProjectID: "p", ChangeID: "1"})
	rc.Source.Findings = []review.Finding{{ID: "F1", Citations: []review.EvidenceCitation{{Source: "contract.md"}}}}
	rc.Source.Run.StepProviders["review"] = "provider/model"
	rc.Source.Coverage = review.CoverageProjection{Checks: []review.Check{{ID: "correctness", EvidenceRefs: []string{"obs-1"}}}}
	if err := store.SaveContext(ctx, run.ID, rc); err != nil {
		t.Fatal(err)
	}

	rc.Source.Findings[0].ID = "mutated-after-save"
	rc.Source.Run.StepProviders["review"] = "mutated-after-save"
	first, err := store.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Source.Findings[0].ID != "F1" || first.Source.Run.StepProviders["review"] != "provider/model" {
		t.Fatalf("store retained caller aliases: %#v", first.Source)
	}

	first.Source.Findings[0].Citations[0].Source = "mutated-after-read"
	first.Source.Coverage.Checks[0].EvidenceRefs[0] = "mutated-after-read"
	second, err := store.Get(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Source.Findings[0].Citations[0].Source != "contract.md" || second.Source.Coverage.Checks[0].EvidenceRefs[0] != "obs-1" {
		t.Fatalf("store exposed mutable source aliases: %#v", second.Source)
	}
}

func TestPathPolicyFilterUsesProfileIgnorePatterns(t *testing.T) {
	filter := PathPolicyFilter{Ignore: []string{"generated/**", "**/*.snap"}}
	rc := review.NewContext(review.Request{
		ChangedPaths: []string{
			"generated/client.go",
			"ui/button.snap",
			"agent/app/server.go",
		},
	})
	decision, err := filter.Apply(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.SkippedPaths) != 2 {
		t.Fatalf("expected profile ignored paths, got %#v", decision)
	}
	if len(decision.ReviewPaths) != 1 || decision.ReviewPaths[0] != "agent/app/server.go" {
		t.Fatalf("expected review path preserved, got %#v", decision)
	}
}
