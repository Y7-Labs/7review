package tools

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"strings"
)

func (r *repositoryHTTP) json(ctx context.Context, endpoint string, out any) error {
	data, _, err := r.get(ctx, endpoint, maxGitMetadataBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s: invalid repository metadata: %w", r.service, err)
	}
	return nil
}

func requireRepositoryObject(mode, kind, id string, directory bool) error {
	if !immutableGitRevisionPattern.MatchString(id) {
		return fmt.Errorf("repository tree returned invalid object identity")
	}
	if directory {
		if mode != "040000" || kind != "tree" {
			return fmt.Errorf("repository path component is not a regular tree")
		}
	} else if (mode != "100644" && mode != "100755") || kind != "blob" {
		return fmt.Errorf("repository file is not a regular blob")
	}
	return nil
}

// Git object identity uses the repository's native object format, not the policy
// artifact digest. SHA-1 here is Git protocol compatibility, not authorization.
func verifyRepositoryBlob(id string, data []byte) error {
	var h hash.Hash
	switch len(id) {
	case 40:
		h = sha1.New()
	case 64:
		h = sha256.New()
	default:
		return fmt.Errorf("repository blob has invalid object identity")
	}
	fmt.Fprintf(h, "blob %d%c", len(data), byte(0))
	h.Write(data)
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), id) {
		return fmt.Errorf("repository blob content does not match tree object identity")
	}
	return nil
}

func decodeRepositoryBlob(content string) ([]byte, error) {
	clean := strings.ReplaceAll(strings.ReplaceAll(content, "\n", ""), "\r", "")
	if base64.StdEncoding.DecodedLen(len(clean)) > repositoryFileReadLimit+2 {
		return nil, fmt.Errorf("repository blob exceeds byte bound")
	}
	data, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("repository blob is not base64: %w", err)
	}
	if len(data) > repositoryFileReadLimit {
		return nil, fmt.Errorf("repository blob exceeds byte bound")
	}
	return data, nil
}
