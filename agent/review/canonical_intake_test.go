package review

import (
	"strings"
	"testing"
	"time"
)

func canonicalIntakeFixture() (Request, AttemptIdentity, ExecutionContext, SnapshotAttestation) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	snapshot := SnapshotIdentity{RepositoryID: "org/repo", BaseRevision: "base", HeadRevision: "head", FileManifestDigest: "sha256:" + strings.Repeat("a", 64)}
	req := Request{Provider: "github", ProjectID: "org/repo", Repository: "org/repo", ChangeID: "7", SourceSHA: "head", TargetSHA: "base"}
	a := AttemptIdentity{AttemptID: "attempt", WorkIntentID: "intent", IntentKind: IntentAutomatic, Change: ChangeKey{Provider: "github", Host: "github.com", RepositoryID: "org/repo", ChangeNumber: "7"}, Snapshot: snapshot, PolicyDigest: "sha256:" + strings.Repeat("b", 64), Generation: 1, Version: 1, State: AttemptAccepted, CreatedAt: now, UpdatedAt: now}
	e := ExecutionContext{Mode: ExecutionService, ProducerID: "service", ComparisonTree: "base::head", Persistence: PersistenceDurableService, TrustedBoundaryID: "operator-boundary", DeadlineAt: now.Add(time.Hour)}
	att := SnapshotAttestation{Snapshot: snapshot, ProducerID: "scm-api", ComparisonTree: e.ComparisonTree, VerifiedAt: now, Verified: true}
	return req, a, e, att
}

func TestVerifiedIntakeRejectsCrossProviderChangeAndRevision(t *testing.T) {
	for _, field := range []string{"provider", "change", "iid", "head", "base", "repository fallback"} {
		t.Run(field, func(t *testing.T) {
			req, a, e, att := canonicalIntakeFixture()
			switch field {
			case "provider":
				req.Provider = "gitlab"
			case "change":
				req.ChangeID = "8"
			case "iid":
				req.MRIID = 8
			case "head":
				req.SourceSHA = "other"
			case "base":
				req.TargetSHA = "other"
			case "repository fallback":
				req.ProjectID = ""
				req.Repository = "other/repo"
			}
			if _, err := NewVerifiedContext(req, a, e, att); err == nil {
				t.Fatal("mismatched request entered canonical execution")
			}
		})
	}
}

func TestVerifiedContextRetainsDetachedAttestation(t *testing.T) {
	req, a, e, att := canonicalIntakeFixture()
	rc, err := NewVerifiedContext(req, a, e, att)
	if err != nil {
		t.Fatal(err)
	}
	if rc.Source.Attestation == nil || rc.Source.Attestation.Snapshot != a.Snapshot {
		t.Fatal("verified proof was discarded")
	}
	att.Snapshot.HeadRevision = "mutated"
	if rc.Source.Attestation.Snapshot.HeadRevision != "head" {
		t.Fatal("caller mutated canonical attestation")
	}
}

func TestSCMAttestationCannotRebindRequestIdentity(t *testing.T) {
	for _, field := range []string{"provider", "repository", "change"} {
		t.Run(field, func(t *testing.T) {
			req, _, _, _ := canonicalIntakeFixture()
			scm := &SCMContext{Provider: "github", ProjectID: "org/repo", ChangeID: "7", DiffRefs: DiffRefs{BaseSHA: "base", HeadSHA: "head"}}
			switch field {
			case "provider":
				scm.Provider = "gitlab"
			case "repository":
				scm.ProjectID = "other/repo"
			case "change":
				scm.ChangeID = "8"
			}
			if _, _, err := AttestSCMRequest(req, scm, time.Now().UTC()); err == nil {
				t.Fatal("SCM response rebound authenticated request")
			}
		})
	}
}
