package review

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

type PublicationDestination struct {
	Provider      string `json:"provider"`
	Host          string `json:"host"`
	RepositoryID  string `json:"repository_id"`
	ChangeNumber  string `json:"change_number"`
	ArtifactClass string `json:"artifact_class"`
}

func (d PublicationDestination) Validate() error {
	for _, value := range []string{d.Provider, d.RepositoryID, d.ChangeNumber} {
		if value != strings.TrimSpace(value) || !validOperatorCommandText(value, 256) {
			return errors.New("invalid publication destination")
		}
	}
	switch d.Provider {
	case "github", "gitlab":
		if d.Host != strings.TrimSpace(d.Host) || !validOperatorCommandText(d.Host, 256) {
			return errors.New("invalid publication destination")
		}
	case "local":
		if d.Host != "" {
			return errors.New("invalid publication destination")
		}
	default:
		return errors.New("invalid publication provider")
	}
	switch d.ArtifactClass {
	case "assessment", "comment", "check", "annotation", "code_quality":
		return nil
	default:
		return errors.New("invalid publication artifact class")
	}
}

type AuthorizePublicationPayload struct {
	AssessmentDigest string                 `json:"assessment_digest"`
	ArtifactDigest   string                 `json:"artifact_digest"`
	Destination      PublicationDestination `json:"destination"`
	Reason           string                 `json:"reason"`
}

// Actor is deliberately absent: only the authenticated transport can supply it.
type AuthorizePublicationCommand struct {
	SchemaVersion   int                         `json:"schema_version"`
	CommandID       string                      `json:"command_id"`
	AttemptID       string                      `json:"attempt_id"`
	ExpectedVersion uint64                      `json:"expected_version"`
	Revision        string                      `json:"revision"`
	Kind            string                      `json:"kind"`
	Payload         AuthorizePublicationPayload `json:"payload"`
}

func (c AuthorizePublicationCommand) Validate() error {
	if c.SchemaVersion != 1 || !operatorID.MatchString(c.CommandID) || !operatorID.MatchString(c.AttemptID) || c.ExpectedVersion == 0 || c.ExpectedVersion >= math.MaxInt64 || c.Kind != "authorize_publication" || !validOperatorCommandText(c.Revision, 256) || !canonicalDigestPattern.MatchString(c.Payload.AssessmentDigest) || !canonicalDigestPattern.MatchString(c.Payload.ArtifactDigest) || !validOperatorCommandText(c.Payload.Reason, 2048) || c.Payload.Destination.Validate() != nil {
		return errors.New("invalid authorize_publication command")
	}
	return nil
}

func DecodeAuthorizePublicationCommand(data []byte) (AuthorizePublicationCommand, error) {
	var c AuthorizePublicationCommand
	invalid := errors.New("invalid authorize_publication envelope")
	if len(data) == 0 || len(data) > MaxOperatorCommandBytes || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil {
		return c, invalid
	}
	if decodeOperatorObject(data, &c, "schema_version", "command_id", "attempt_id", "expected_version", "revision", "kind", "payload") != nil {
		return AuthorizePublicationCommand{}, invalid
	}
	var raw struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(data, &raw) != nil || decodeOperatorObject(raw.Payload, &c.Payload, "assessment_digest", "artifact_digest", "destination", "reason") != nil {
		return AuthorizePublicationCommand{}, invalid
	}
	var nested struct {
		Destination json.RawMessage `json:"destination"`
	}
	if json.Unmarshal(raw.Payload, &nested) != nil || decodeOperatorObject(nested.Destination, &c.Payload.Destination, "provider", "host", "repository_id", "change_number", "artifact_class") != nil {
		return AuthorizePublicationCommand{}, invalid
	}
	if err := c.Validate(); err != nil {
		return AuthorizePublicationCommand{}, err
	}
	return c, nil
}

type PublicationAuthorizationReceipt struct {
	SchemaVersion    int                    `json:"schema_version"`
	CommandID        string                 `json:"command_id"`
	CommandDigest    string                 `json:"command_digest"`
	AttemptID        string                 `json:"attempt_id"`
	ActorID          string                 `json:"actor_id"`
	Kind             string                 `json:"kind"`
	Revision         string                 `json:"revision"`
	ControlVersion   uint64                 `json:"control_version"`
	AssessmentDigest string                 `json:"assessment_digest"`
	ArtifactDigest   string                 `json:"artifact_digest"`
	Destination      PublicationDestination `json:"destination"`
	State            string                 `json:"state"`
	CreatedAt        time.Time              `json:"created_at"`
}

func DecodePublicationAuthorizationReceipt(data []byte, c AuthorizePublicationCommand) (PublicationAuthorizationReceipt, error) {
	var r PublicationAuthorizationReceipt
	invalid := errors.New("invalid publication authorization receipt")
	if c.Validate() != nil || len(data) > 16384 || rejectDuplicateJSONKeys(data) != nil || decodeOperatorObject(data, &r, "schema_version", "command_id", "command_digest", "attempt_id", "actor_id", "kind", "revision", "control_version", "assessment_digest", "artifact_digest", "destination", "state", "created_at") != nil {
		return PublicationAuthorizationReceipt{}, invalid
	}
	var raw struct {
		Destination json.RawMessage `json:"destination"`
	}
	if json.Unmarshal(data, &raw) != nil || decodeOperatorObject(raw.Destination, &r.Destination, "provider", "host", "repository_id", "change_number", "artifact_class") != nil {
		return PublicationAuthorizationReceipt{}, invalid
	}
	if r.SchemaVersion != 1 || r.CommandID != c.CommandID || r.AttemptID != c.AttemptID || r.Kind != c.Kind || r.Revision != c.Revision || r.ControlVersion != c.ExpectedVersion+1 || r.AssessmentDigest != c.Payload.AssessmentDigest || r.ArtifactDigest != c.Payload.ArtifactDigest || r.Destination != c.Payload.Destination || r.State != "authorized" || !operatorID.MatchString(r.ActorID) || !canonicalDigestPattern.MatchString(r.CommandDigest) || r.CreatedAt.IsZero() {
		return PublicationAuthorizationReceipt{}, invalid
	}
	return r, nil
}
