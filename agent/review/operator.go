package review

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

type OperatorRole string

const (
	OperatorRoleOperator      OperatorRole = "operator"
	OperatorRoleReviewer      OperatorRole = "reviewer"
	OperatorRolePublisher     OperatorRole = "publisher"
	OperatorRoleParticipant   OperatorRole = "participant"
	OperatorRoleMemoryCurator OperatorRole = "memory_curator"
)

var ErrOperatorUnauthorized = errors.New("operator authentication required")
var ErrOperatorForbidden = errors.New("operator authority denied")

// OperatorActor can only be issued by a trusted directory after authentication.
// A principal ID supplied by a client is not an authenticated actor.
type OperatorActor struct {
	directory *OperatorDirectory
	principal string
}

func (a OperatorActor) PrincipalID() string { return a.principal }

type operatorGrant struct {
	BoundaryID   string         `json:"boundary_id"`
	ProjectID    string         `json:"project_id"`
	Provider     string         `json:"provider"`
	Host         string         `json:"host"`
	RepositoryID string         `json:"repository_id"`
	Roles        []OperatorRole `json:"roles"`
}

type operatorPrincipal struct {
	id        string
	tokenHash [32]byte
	grants    []operatorGrant
}

// OperatorDirectory is immutable host authority. It never serializes credentials
// or accepts request-provided roles. Sharing a pointer pins actor provenance.
type OperatorDirectory struct{ principals []operatorPrincipal }

var operatorID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
var operatorEnv = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
var operatorHost = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?::[0-9]{1,5})?$`)

func DecodeOperatorDirectory(data []byte, getenv func(string) string) (*OperatorDirectory, error) {
	invalid := errors.New("invalid operator directory configuration")
	if len(data) == 0 || len(data) > 64<<10 || !utf8.Valid(data) || getenv == nil || rejectDuplicateJSONKeys(data) != nil {
		return nil, invalid
	}
	var doc struct {
		SchemaVersion int               `json:"schema_version"`
		Principals    []json.RawMessage `json:"principals"`
	}
	if decodeOperatorObject(data, &doc, "schema_version", "principals") != nil || doc.SchemaVersion != 1 || len(doc.Principals) == 0 || len(doc.Principals) > 64 {
		return nil, invalid
	}
	d := &OperatorDirectory{}
	ids := map[string]bool{}
	envs := map[string]bool{}
	hashes := map[[32]byte]bool{}
	for _, raw := range doc.Principals {
		var p struct {
			ID       string            `json:"id"`
			TokenEnv string            `json:"token_env"`
			Grants   []json.RawMessage `json:"grants"`
		}
		if decodeOperatorObject(raw, &p, "id", "token_env", "grants") != nil || !operatorID.MatchString(p.ID) || !operatorEnv.MatchString(p.TokenEnv) || ids[p.ID] || envs[p.TokenEnv] || len(p.Grants) == 0 || len(p.Grants) > 128 {
			return nil, invalid
		}
		token := getenv(p.TokenEnv)
		if !validOperatorCredential(token) {
			return nil, invalid
		}
		h := sha256.Sum256([]byte(token))
		if hashes[h] {
			return nil, invalid
		}
		ids[p.ID], envs[p.TokenEnv], hashes[h] = true, true, true
		principal := operatorPrincipal{id: p.ID, tokenHash: h}
		seen := map[string]bool{}
		for _, raw := range p.Grants {
			var g operatorGrant
			if decodeOperatorObject(raw, &g, "boundary_id", "project_id", "provider", "host", "repository_id", "roles") != nil || !operatorID.MatchString(g.BoundaryID) || !operatorID.MatchString(g.ProjectID) || !operatorID.MatchString(g.RepositoryID) || len(g.Roles) == 0 {
				return nil, invalid
			}
			if g.Provider != "github" && g.Provider != "gitlab" && g.Provider != "local" {
				return nil, invalid
			}
			if g.Provider == "local" {
				if g.Host != "" {
					return nil, invalid
				}
			} else if !operatorHost.MatchString(g.Host) {
				return nil, invalid
			}
			for _, role := range g.Roles {
				switch role {
				case OperatorRoleOperator, OperatorRoleReviewer, OperatorRolePublisher, OperatorRoleParticipant, OperatorRoleMemoryCurator:
				default:
					return nil, invalid
				}
				key := strings.Join([]string{g.BoundaryID, g.ProjectID, g.Provider, g.Host, g.RepositoryID, string(role)}, "\x00")
				if seen[key] {
					return nil, invalid
				}
				seen[key] = true
			}
			principal.grants = append(principal.grants, g)
		}
		d.principals = append(d.principals, principal)
	}
	return d, nil
}

func decodeOperatorObject(data []byte, target any, fields ...string) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) != len(fields) {
		return errors.New("invalid operator object fields")
	}
	for _, key := range fields {
		value, ok := raw[key]
		if !ok || string(value) == "null" {
			return errors.New("missing operator field")
		}
	}
	return json.Unmarshal(data, target)
}

func validOperatorCredential(token string) bool {
	if len(token) == 0 || len(token) > 4096 {
		return false
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func (d *OperatorDirectory) Authenticate(token string) (OperatorActor, error) {
	if d == nil || !validOperatorCredential(token) {
		return OperatorActor{}, ErrOperatorUnauthorized
	}
	h := sha256.Sum256([]byte(token))
	id := ""
	for _, p := range d.principals {
		if subtle.ConstantTimeCompare(h[:], p.tokenHash[:]) == 1 {
			id = p.id
		}
	}
	if id == "" {
		return OperatorActor{}, ErrOperatorUnauthorized
	}
	return OperatorActor{directory: d, principal: id}, nil
}

func (d *OperatorDirectory) Authorize(actor OperatorActor, boundary, project string, change ChangeKey, roles ...OperatorRole) error {
	if err := d.ValidateActor(actor); err != nil {
		return err
	}
	for _, p := range d.principals {
		if p.id != actor.principal {
			continue
		}
		for _, g := range p.grants {
			if g.BoundaryID != boundary || g.ProjectID != project || g.Provider != change.Provider || g.Host != change.Host || g.RepositoryID != change.RepositoryID {
				continue
			}
			for _, required := range roles {
				for _, granted := range g.Roles {
					if required == granted {
						return nil
					}
				}
			}
		}
	}
	return ErrOperatorForbidden
}

func (d *OperatorDirectory) ValidateActor(actor OperatorActor) error {
	if d == nil || actor.directory != d || actor.principal == "" {
		return ErrOperatorUnauthorized
	}
	for _, p := range d.principals {
		if p.id == actor.principal {
			return nil
		}
	}
	return ErrOperatorUnauthorized
}
