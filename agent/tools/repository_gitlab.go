package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type gitLabRepositoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	ID   string `json:"id"`
}

func readGitLabRepositoryBlob(ctx context.Context, r *repositoryHTTP, repository, revision, filePath string) ([]byte, error) {
	prefix := "/api/v4/projects/" + url.PathEscape(repository) + "/repository/"
	parts := strings.Split(filePath, "/")
	dir := ""
	for i, part := range parts {
		entry, err := gitLabRepositoryTreeEntry(ctx, r, prefix, revision, dir, part)
		if err != nil {
			return nil, err
		}
		directory := i < len(parts)-1
		if err := requireRepositoryObject(entry.Mode, entry.Type, entry.ID, directory); err != nil {
			return nil, err
		}
		if directory {
			dir = entry.Path
			continue
		}
		data, _, err := r.get(ctx, prefix+"blobs/"+entry.ID+"/raw", repositoryFileReadLimit)
		if err != nil {
			return nil, err
		}
		if err := verifyRepositoryBlob(entry.ID, data); err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("gitlab: empty repository path")
}

func gitLabRepositoryTreeEntry(ctx context.Context, r *repositoryHTTP, prefix, revision, dir, name string) (gitLabRepositoryEntry, error) {
	wanted := name
	if dir != "" {
		wanted = dir + "/" + name
	}
	var selected *gitLabRepositoryEntry
	for page := 1; ; page++ {
		query := url.Values{"ref": {revision}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if dir != "" {
			query.Set("path", dir)
		}
		data, headers, err := r.get(ctx, prefix+"tree?"+query.Encode(), maxGitMetadataBytes)
		if err != nil {
			return gitLabRepositoryEntry{}, err
		}
		var entries []gitLabRepositoryEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			return gitLabRepositoryEntry{}, fmt.Errorf("gitlab: invalid repository tree: %w", err)
		}
		if len(entries) > 100 {
			return gitLabRepositoryEntry{}, fmt.Errorf("gitlab: repository tree page exceeds entry bound")
		}
		for _, entry := range entries {
			if entry.Path != wanted {
				continue
			}
			if entry.Name != name || selected != nil {
				return gitLabRepositoryEntry{}, fmt.Errorf("gitlab: ambiguous repository tree path")
			}
			copyEntry := entry
			selected = &copyEntry
		}
		next := headers.Get("X-Next-Page")
		if next != "" {
			n, err := strconv.Atoi(next)
			if err != nil || n != page+1 {
				return gitLabRepositoryEntry{}, fmt.Errorf("gitlab: invalid repository pagination")
			}
			continue
		}
		if nextGitHubPage("", headers) != "" {
			if len(headers.Values("X-Next-Page")) > 0 {
				return gitLabRepositoryEntry{}, fmt.Errorf("gitlab: contradictory repository pagination")
			}
			continue
		}
		// Reconstruct page requests rather than following provider-supplied URLs.
		// Without pagination headers, a full page requires one further bounded read.
		if len(headers.Values("X-Next-Page")) == 0 && len(entries) == 100 {
			continue
		}
		break
	}
	if selected == nil {
		return gitLabRepositoryEntry{}, fmt.Errorf("%w: %s", ErrRepositoryFileNotFound, wanted)
	}
	return *selected, nil
}
