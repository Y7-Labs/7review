package tools

import (
	"context"
	"fmt"
	"strings"
)

type gitHubRepositoryEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size *int64 `json:"size"`
}

func readGitHubRepositoryBlob(ctx context.Context, r *repositoryHTTP, repository, revision, filePath string) ([]byte, error) {
	prefix := "/repos/" + repoPath(repository) + "/git/"
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := r.json(ctx, prefix+"commits/"+revision, &commit); err != nil {
		return nil, err
	}
	if !strings.EqualFold(commit.SHA, revision) || !immutableGitRevisionPattern.MatchString(commit.Tree.SHA) {
		return nil, fmt.Errorf("github: commit response does not bind requested revision/tree")
	}
	treeID := commit.Tree.SHA
	parts := strings.Split(filePath, "/")
	for i, part := range parts {
		var tree struct {
			SHA       string                  `json:"sha"`
			Truncated bool                    `json:"truncated"`
			Entries   []gitHubRepositoryEntry `json:"tree"`
		}
		if err := r.json(ctx, prefix+"trees/"+treeID, &tree); err != nil {
			return nil, err
		}
		if !strings.EqualFold(tree.SHA, treeID) || tree.Truncated {
			return nil, fmt.Errorf("github: tree identity mismatch or truncated repository tree")
		}
		var selected *gitHubRepositoryEntry
		for j := range tree.Entries {
			if tree.Entries[j].Path == part {
				if selected != nil {
					return nil, fmt.Errorf("github: ambiguous duplicate repository path")
				}
				selected = &tree.Entries[j]
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("%w: %s", ErrRepositoryFileNotFound, filePath)
		}
		directory := i < len(parts)-1
		if err := requireRepositoryObject(selected.Mode, selected.Type, selected.SHA, directory); err != nil {
			return nil, err
		}
		if directory {
			treeID = selected.SHA
			continue
		}
		if selected.Size == nil || *selected.Size < 0 || *selected.Size > repositoryFileReadLimit {
			return nil, fmt.Errorf("github: invalid or oversized repository blob size")
		}
		var blob struct {
			SHA      string `json:"sha"`
			Size     *int64 `json:"size"`
			Encoding string `json:"encoding"`
			Content  string `json:"content"`
		}
		if err := r.json(ctx, prefix+"blobs/"+selected.SHA, &blob); err != nil {
			return nil, err
		}
		if !strings.EqualFold(blob.SHA, selected.SHA) || blob.Encoding != "base64" || blob.Size == nil || *blob.Size != *selected.Size {
			return nil, fmt.Errorf("github: blob receipt does not match selected tree entry")
		}
		data, err := decodeRepositoryBlob(blob.Content)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != *selected.Size {
			return nil, fmt.Errorf("github: blob byte count does not match tree metadata")
		}
		if err := verifyRepositoryBlob(selected.SHA, data); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("github: empty repository path")
}
