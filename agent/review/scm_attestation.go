package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func AttestSCMRequest(req Request, scm *SCMContext, verifiedAt time.Time) (ChangeKey, SnapshotAttestation, error) {
	if scm == nil {
		return ChangeKey{}, SnapshotAttestation{}, fmt.Errorf("SCM context is required")
	}
	repositoryID := strings.TrimSpace(firstNonEmptyValue(scm.ProjectID, scm.Repository, req.ProjectID, req.Repository))
	base := strings.TrimSpace(firstNonEmptyValue(scm.DiffRefs.BaseSHA, req.TargetSHA))
	head := strings.TrimSpace(firstNonEmptyValue(scm.DiffRefs.HeadSHA, req.SourceSHA))
	if req.SourceSHA != "" && scm.DiffRefs.HeadSHA != "" && req.SourceSHA != scm.DiffRefs.HeadSHA {
		return ChangeKey{}, SnapshotAttestation{}, fmt.Errorf("webhook head revision does not match SCM head revision")
	}
	if req.TargetSHA != "" && scm.DiffRefs.BaseSHA != "" && req.TargetSHA != scm.DiffRefs.BaseSHA {
		return ChangeKey{}, SnapshotAttestation{}, fmt.Errorf("webhook base revision does not match SCM base revision")
	}
	if base == "" || head == "" {
		return ChangeKey{}, SnapshotAttestation{}, fmt.Errorf("SCM base and head revisions are required")
	}
	manifestDigest, err := changedFileManifestDigest(scm.Files)
	if err != nil {
		return ChangeKey{}, SnapshotAttestation{}, err
	}
	change := ChangeKey{
		Provider: strings.ToLower(strings.TrimSpace(req.Provider)), Host: requestHost(req), RepositoryID: repositoryID,
		ChangeNumber: firstNonEmptyValue(req.ChangeID, strconv.Itoa(req.MRIID)),
	}
	comparisonTree := strings.Join([]string{base, scm.DiffRefs.StartSHA, head}, ":")
	attestation := SnapshotAttestation{
		Snapshot:   SnapshotIdentity{RepositoryID: repositoryID, BaseRevision: base, HeadRevision: head, FileManifestDigest: manifestDigest},
		ProducerID: change.Provider + ":scm-api", ComparisonTree: comparisonTree, VerifiedAt: verifiedAt.UTC(), Verified: true,
	}
	if err := attestation.Validate(change); err != nil {
		return ChangeKey{}, SnapshotAttestation{}, err
	}
	return change, attestation, nil
}

func changedFileManifestDigest(files []ChangedFile) (string, error) {
	type manifestEntry struct {
		OldPath     string `json:"old_path,omitempty"`
		NewPath     string `json:"new_path"`
		Status      string `json:"status"`
		Additions   int    `json:"additions"`
		Deletions   int    `json:"deletions"`
		PatchDigest string `json:"patch_digest"`
	}
	entries := make([]manifestEntry, 0, len(files))
	for _, file := range files {
		patch := sha256.Sum256([]byte(file.Patch))
		entries = append(entries, manifestEntry{
			OldPath: file.OldPath, NewPath: file.NewPath, Status: file.Status,
			Additions: file.Additions, Deletions: file.Deletions,
			PatchDigest: "sha256:" + hex.EncodeToString(patch[:]),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].NewPath != entries[j].NewPath {
			return entries[i].NewPath < entries[j].NewPath
		}
		return entries[i].OldPath < entries[j].OldPath
	})
	data, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode changed file manifest: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func requestHost(req Request) string {
	if parsed, err := url.Parse(req.WebURL); err == nil && parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	switch strings.ToLower(strings.TrimSpace(req.Provider)) {
	case "github":
		return "github.com"
	case "gitlab":
		return "gitlab"
	default:
		return strings.ToLower(strings.TrimSpace(req.Provider))
	}
}

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
