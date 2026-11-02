package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommittedModelReferenceIsDetachedAndNotSerializedAsAuthority(t *testing.T) {
	rc := NewContext(Request{})
	rc.RecordModelResult(ModelCallRef{ActionID: "private-parent-action", RequestDigest: "request", ResponseDigest: "response", Step: "step5"})
	cloned := rc.Clone()
	ref := cloned.LatestModelResult()
	ref.ActionID = "changed"
	rc.RecordModelResult(ModelCallRef{ActionID: "new-result"})
	if cloned.LatestModelResult().ActionID != "private-parent-action" || rc.LatestModelResult().ActionID != "new-result" {
		t.Fatal("transient result reference aliases another context")
	}
	raw, err := json.Marshal(cloned.Source)
	if err != nil || strings.Contains(string(raw), "private-parent-action") {
		t.Fatalf("transient action handle leaked into canonical source: %v", err)
	}
}
