package review

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const publicationCommandFixture = `{"schema_version":1,"command_id":"publish-1","attempt_id":"attempt-1","expected_version":2,"revision":"head","kind":"authorize_publication","payload":{"assessment_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifact_digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","destination":{"provider":"github","host":"github.com","repository_id":"org/repo","change_number":"7","artifact_class":"assessment"},"reason":"reviewed exact assessment"}}`

func TestAuthorizePublicationCommandRequiresExactClosedTarget(t *testing.T) {
	c, err := DecodeAuthorizePublicationCommand([]byte(publicationCommandFixture))
	if err != nil || c.Kind != "authorize_publication" || c.Payload.Destination.RepositoryID != "org/repo" {
		t.Fatalf("command: %+v %v", c, err)
	}
	for _, bad := range []string{
		"{}",
		publicationCommandFixture + "{}",
		strings.Replace(publicationCommandFixture, `"expected_version":2`, `"expected_version":0`, 1),
		strings.Replace(publicationCommandFixture, `"kind":"authorize_publication"`, `"kind":"cancel_attempt"`, 1),
		strings.Replace(publicationCommandFixture, `"artifact_class":"assessment"`, `"artifact_class":"merge"`, 1),
		strings.Replace(publicationCommandFixture, `"assessment_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"assessment_digest":"latest"`, 1),
		strings.Replace(publicationCommandFixture, `"reason":"reviewed exact assessment"`, `"reason":"reviewed exact assessment","reason":"other"`, 1),
		strings.Replace(publicationCommandFixture, `"destination":{`, `"destination":{"extra":true,`, 1),
	} {
		if _, err := DecodeAuthorizePublicationCommand([]byte(bad)); err == nil {
			t.Fatal("ambiguous publication authority accepted")
		}
	}
}

func TestPublicationAuthorizationReceiptMustAcknowledgeExactCommand(t *testing.T) {
	c, err := DecodeAuthorizePublicationCommand([]byte(publicationCommandFixture))
	if err != nil {
		t.Fatal(err)
	}
	r := PublicationAuthorizationReceipt{
		SchemaVersion: 1, CommandID: c.CommandID, CommandDigest: "sha256:" + strings.Repeat("c", 64), AttemptID: c.AttemptID,
		ActorID: "alice", Kind: c.Kind, Revision: c.Revision, ControlVersion: 3, AssessmentDigest: c.Payload.AssessmentDigest,
		ArtifactDigest: c.Payload.ArtifactDigest, Destination: c.Payload.Destination, State: "authorized", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodePublicationAuthorizationReceipt(raw, c); err != nil || got != r {
		t.Fatalf("receipt: %+v %v", got, err)
	}
	for _, bad := range [][]byte{
		[]byte("{}"),
		[]byte(strings.Replace(string(raw), `"artifact_digest":"`+c.Payload.ArtifactDigest+`"`, `"artifact_digest":"sha256:`+strings.Repeat("d", 64)+`"`, 1)),
		[]byte(strings.Replace(string(raw), `"control_version":3`, `"control_version":4`, 1)),
		[]byte(strings.Replace(string(raw), `"state":"authorized"`, `"state":"published"`, 1)),
	} {
		if _, err := DecodePublicationAuthorizationReceipt(bad, c); err == nil {
			t.Fatal("unproven publication receipt accepted")
		}
	}
}
