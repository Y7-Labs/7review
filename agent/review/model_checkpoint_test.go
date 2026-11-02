package review

import (
	"context"
	"strings"
	"testing"
)

func TestModelCheckpointFreezesEvidenceResponsesAndCursor(t *testing.T) {
	rc := NewContext(Request{ProjectID: "org/repo"})
	rc.Source.Evidence = []EvidenceItem{{Source: "contract.md"}}
	rc.AddFindings("earlier response")
	contract := &ModelValidation{Version: 1, MinConfidence: 0.8}
	validationCtx := WithModelValidation(context.Background(), contract)
	contract.MinConfidence = 0.1
	ctx, err := WithModelCheckpoint(validationCtx, rc, ModelCursor{Phase: ModelReviewBatches, BatchCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	rc.Source.Evidence[0].Source = "changed.md"
	rc.AddFindings("later response")
	ctx, err = WithModelBatch(ctx, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	raw := ModelCheckpointFromContext(ctx)
	cp, err := DecodeModelCheckpoint(raw)
	if err != nil || cp.Source.Evidence[0].Source != "contract.md" || len(cp.RawResponses) != 1 || cp.RawResponses[0] != "earlier response" || cp.Cursor.BatchIndex != 1 || cp.Cursor.BatchCount != 2 || cp.Validation == nil || cp.Validation.MinConfidence != 0.8 {
		t.Fatalf("checkpoint changed: %#v %v", cp, err)
	}
	raw[0] = 'x'
	if _, err = DecodeModelCheckpoint(ModelCheckpointFromContext(ctx)); err != nil {
		t.Fatal("checkpoint storage exposed mutable bytes")
	}
	if _, err = WithModelBatch(ctx, 0, 3); err == nil {
		t.Fatal("batch plan changed after checkpoint")
	}
}

func TestModelCheckpointRejectsUnknownOrContradictoryCursors(t *testing.T) {
	for _, cursor := range []ModelCursor{
		{Phase: "unknown"},
		{Phase: ModelReviewBatches, BatchCount: 0},
		{Phase: ModelReviewBatches, BatchCount: 2, BatchIndex: 2},
		{Phase: ModelReviewBatches, BatchCount: 2, Round: 1},
		{Phase: ModelToolRound, Round: 0},
		{Phase: ModelRepairFindings, BatchCount: 1},
	} {
		if _, err := WithModelCheckpoint(context.Background(), NewContext(Request{}), cursor); err == nil {
			t.Fatalf("invalid cursor accepted: %#v", cursor)
		}
	}
	if _, err := DecodeModelCheckpoint([]byte(`{"Version":2}`)); err == nil {
		t.Fatal("unknown checkpoint version accepted")
	}
	if _, err := DecodeModelCheckpoint([]byte(`{"Version":1,"Unknown":true}`)); err == nil {
		t.Fatal("unknown fields accepted")
	}
	rc := NewContext(Request{})
	rc.AddFindings(strings.Repeat("x", MaxModelCheckpointBytes))
	if _, err := WithModelCheckpoint(context.Background(), rc, ModelCursor{Phase: ModelRepairFindings}); err == nil {
		t.Fatal("oversized checkpoint accepted")
	}
}
