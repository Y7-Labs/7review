package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

func TestRoutingIsDetachedAndLegacyCheckpointGroupDigestsRemainStable(t *testing.T) {
	rc := NewContext(Request{})
	ctx, err := WithModelCheckpoint(context.Background(), rc, ModelCursor{Phase: ModelReviewBatches, BatchCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw := ModelCheckpointFromContext(ctx)
	cp, err := DecodeModelCheckpoint(raw)
	if err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		Version      int
		Cursor       ModelCursor
		Source       Source
		RawResponses []string
		Validation   *ModelValidation
	}{cp.Version, cp.Cursor, cp.Source, cp.RawResponses, cp.Validation}
	oldBytes, _ := json.Marshal(legacy)
	digest := sha256.Sum256(oldBytes)
	group, err := ModelCheckpointGroupDigest(raw)
	if err != nil || group != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatalf("optional routing rewrote legacy group identity: %s %v", group, err)
	}
	routing := &ModelRouting{Version: 1, Roles: map[string]ModelRouteChain{"reasoner": {Routes: []ModelRoute{{Provider: "old", Model: "pinned"}}, MaxTokens: 20}}}
	frozen := WithModelRouting(context.Background(), routing)
	chain := routing.Roles["reasoner"]
	chain.Routes[0].Model = "changed"
	routing.Roles["reasoner"] = chain
	ctx, err = WithModelCheckpoint(frozen, rc, ModelCursor{Phase: ModelReviewBatches, BatchCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	cp, err = DecodeModelCheckpoint(ModelCheckpointFromContext(ctx))
	if err != nil || cp.Routing.Roles["reasoner"].Routes[0].Model != "pinned" {
		t.Fatalf("routing context aliases mutable config: %#v %v", cp.Routing, err)
	}
}
