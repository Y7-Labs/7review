package review

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func findingDispositionViewFixture(t *testing.T) ([]byte, FindingDispositionView) {
	t.Helper()
	finding := Finding{ID: "F-1", Severity: SeverityHigh, Title: "Authorization bypass", Description: "The guard is skipped.", Suggestion: "Enforce the guard.", Location: Location{Path: "service.go", Line: 7}, Confidence: 0.91}
	digest, err := CanonicalFindingDigest(finding)
	if err != nil {
		t.Fatal(err)
	}
	view := FindingDispositionView{SchemaVersion: 1, AttemptID: "attempt-1", Revision: "head", ControlVersion: 4, Finding: finding, FindingDigest: digest, Current: &CurrentFindingDisposition{CommandID: "finding-2", ActorID: "alice", Decision: FindingRejected, Reason: "not actionable in this change", RecordedVersion: 4, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	return raw, view
}

func TestFindingDispositionViewRequiresExactClosedSubjectAndAttribution(t *testing.T) {
	raw, want := findingDispositionViewFixture(t)
	got, err := DecodeFindingDispositionView(raw, want.AttemptID, want.Finding.ID)
	if err != nil || got.FindingDigest != want.FindingDigest || got.Current == nil || got.Current.ActorID != "alice" {
		t.Fatalf("view: %+v %v raw=%s", got, err, raw)
	}
	without := want
	without.Current = nil
	raw, _ = json.Marshal(without)
	if got, err = DecodeFindingDispositionView(raw, want.AttemptID, want.Finding.ID); err != nil || got.Current != nil {
		t.Fatalf("undispositioned view: %+v %v", got, err)
	}
}

func TestFindingDispositionViewRejectsAmbiguousOrChangedEvidence(t *testing.T) {
	raw, want := findingDispositionViewFixture(t)
	text := string(raw)
	for _, bad := range [][]byte{
		{},
		append(append([]byte{}, raw...), raw...),
		[]byte(strings.Replace(text, `"attempt_id":"attempt-1"`, `"attempt_id":"other"`, 1)),
		[]byte(strings.Replace(text, `"ID":"F-1"`, `"ID":"F-2"`, 1)),
		[]byte(strings.Replace(text, `"Title":"Authorization bypass"`, `"Title":"Changed"`, 1)),
		[]byte(strings.Replace(text, `"control_version":4`, `"control_version":0`, 1)),
		[]byte(strings.Replace(text, `"decision":"rejected"`, `"decision":"suppressed"`, 1)),
		[]byte(strings.Replace(text, `"actor_id":"alice"`, `"actor_id":"alice","actor_id":"mallory"`, 1)),
		[]byte(strings.Replace(text, `"finding_digest":"`, `"unknown":true,"finding_digest":"`, 1)),
		[]byte(strings.Repeat("x", MaxFindingDispositionViewBytes+1)),
	} {
		if got, err := DecodeFindingDispositionView(bad, want.AttemptID, want.Finding.ID); err == nil || got.AttemptID != "" {
			t.Fatalf("unsafe view accepted: %+v %v", got, err)
		}
	}
}
