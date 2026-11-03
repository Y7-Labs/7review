package review

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestCanonicalFindingDigestBindsCompleteFinding(t *testing.T) {
	finding := Finding{ID: "F-1", Severity: SeverityHigh, Title: "Authorization bypass", Description: "The guard is skipped.", Suggestion: "Enforce the guard.", Location: Location{Path: "auth.go", Line: 42}, Confidence: 0.91, FindingType: "correctness", Strength: "must", EvidenceAuthority: "policy", Citations: []EvidenceCitation{{Source: "auth.go", Rule: "AUTH-1", Violation: "guard skipped"}}, ValidationStatus: "validated", ValidationReason: "direct branch evidence"}
	first, err := CanonicalFindingDigest(finding)
	if err != nil || !canonicalDigestPattern.MatchString(first) {
		t.Fatalf("canonical digest: %q %v", first, err)
	}
	second, err := CanonicalFindingDigest(finding)
	if err != nil || second != first {
		t.Fatalf("identical finding digest drifted: %q %q %v", first, second, err)
	}
	changed := finding
	changed.Citations = append([]EvidenceCitation(nil), finding.Citations...)
	changed.Citations[0].Violation = "different evidence"
	if digest, err := CanonicalFindingDigest(changed); err != nil || digest == first {
		t.Fatalf("changed evidence retained subject digest: %q %v", digest, err)
	}
	invalid := finding
	invalid.Confidence = math.NaN()
	if digest, err := CanonicalFindingDigest(invalid); err == nil || digest != "" {
		t.Fatalf("non-canonical finding produced digest: %q %v", digest, err)
	}
}

const findingDispositionFixture = `{"schema_version":1,"command_id":"finding-1","attempt_id":"attempt-1","expected_version":3,"revision":"head","kind":"disposition_finding","payload":{"finding_id":"F-1","finding_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","decision":"disputed","reason":"the cited branch is unreachable"}}`

func TestFindingDispositionCommandRequiresClosedVersionedSubject(t *testing.T) {
	c, err := DecodeFindingDispositionCommand([]byte(findingDispositionFixture))
	if err != nil || c.CommandID != "finding-1" || c.Payload.Decision != FindingDisputed || c.Payload.FindingID != "F-1" {
		t.Fatalf("command: %+v %v", c, err)
	}
	for n, raw := range []string{
		"{}", findingDispositionFixture + "{}",
		strings.Replace(findingDispositionFixture, `"expected_version":3`, `"expected_version":0`, 1),
		strings.Replace(findingDispositionFixture, `"command_id":"finding-1"`, `"command_id":"finding-1","actor_id":"alice"`, 1),
		strings.Replace(findingDispositionFixture, `"kind":"disposition_finding"`, `"kind":"suppress"`, 1),
		strings.Replace(findingDispositionFixture, `"decision":"disputed"`, `"decision":"suppressed"`, 1),
		strings.Replace(findingDispositionFixture, `"finding_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"finding_digest":"latest"`, 1),
		strings.Replace(findingDispositionFixture, `"finding_id":"F-1"`, `"finding_id":"  "`, 1),
		strings.Replace(findingDispositionFixture, `"reason":"the cited branch is unreachable"`, `"reason":"  "`, 1),
		strings.Replace(findingDispositionFixture, `"reason":"the cited branch is unreachable"`, `"reason":"x","extra":true`, 1),
		strings.Replace(findingDispositionFixture, `"payload":{`, `"payload":{"actor":"alice",`, 1),
		strings.Replace(findingDispositionFixture, `"expected_version":3`, `"expected_version":3,"expected_version":4`, 1),
		strings.Repeat(" ", MaxOperatorCommandBytes+1),
	} {
		if _, err := DecodeFindingDispositionCommand([]byte(raw)); err == nil {
			t.Errorf("unsafe finding disposition %d accepted", n)
		}
	}
	for _, decision := range []FindingDecision{FindingAccepted, FindingRejected, FindingDisputed} {
		candidate := c
		candidate.Payload.Decision = decision
		if err := candidate.Validate(); err != nil {
			t.Fatalf("decision %q rejected: %v", decision, err)
		}
	}
}

func TestFindingDispositionReceiptMustAcknowledgeExactSubject(t *testing.T) {
	c, err := DecodeFindingDispositionCommand([]byte(findingDispositionFixture))
	if err != nil {
		t.Fatal(err)
	}
	r := FindingDispositionReceipt{
		SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: "sha256:" + strings.Repeat("c", 64), AttemptID: c.AttemptID,
		ActorID: "alice", Kind: c.Kind, Revision: c.Revision, ControlVersion: 4, FindingID: c.Payload.FindingID,
		FindingDigest: c.Payload.FindingDigest, Decision: c.Payload.Decision, State: "recorded", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeFindingDispositionReceipt(raw, c); err != nil || got != r {
		t.Fatalf("receipt: %+v %v", got, err)
	}
	for _, bad := range [][]byte{
		[]byte("{}"),
		[]byte(strings.Replace(string(raw), `"finding_id":"F-1"`, `"finding_id":"F-2"`, 1)),
		[]byte(strings.Replace(string(raw), `"decision":"disputed"`, `"decision":"rejected"`, 1)),
		[]byte(strings.Replace(string(raw), `"control_version":4`, `"control_version":5`, 1)),
		[]byte(strings.Replace(string(raw), `"state":"recorded"`, `"state":"applied"`, 1)),
	} {
		if _, err := DecodeFindingDispositionReceipt(bad, c); err == nil {
			t.Fatal("unproven finding disposition receipt accepted")
		}
	}
}
