package tools

import (
	"context"
	"fmt"
	"strings"
)

func readFrozenRepositoryBlob(ctx context.Context, g FrozenGitSCM, revision, filePath string) ([]byte, error) {
	root, err := g.run(ctx, 1024, "rev-parse", "--verify", revision+"^{tree}")
	if err != nil {
		return nil, err
	}
	treeID := strings.TrimSpace(string(root))
	if !immutableGitRevisionPattern.MatchString(treeID) {
		return nil, fmt.Errorf("git: invalid immutable tree identity")
	}
	parts := strings.Split(filePath, "/")
	for i, part := range parts {
		data, err := g.run(ctx, 8192, "ls-tree", "-z", "--full-tree", treeID, "--", part)
		if err != nil {
			return nil, err
		}
		records := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		if len(data) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrRepositoryFileNotFound, filePath)
		}
		if len(records) != 1 {
			return nil, fmt.Errorf("git: ambiguous repository tree path")
		}
		metadata, name, ok := strings.Cut(records[0], "\t")
		fields := strings.Fields(metadata)
		if !ok || name != part || len(fields) != 3 {
			return nil, fmt.Errorf("git: invalid repository tree entry")
		}
		directory := i < len(parts)-1
		if err := requireRepositoryObject(fields[0], fields[1], fields[2], directory); err != nil {
			return nil, err
		}
		if directory {
			treeID = fields[2]
			continue
		}
		blob, err := g.run(ctx, repositoryFileReadLimit, "cat-file", "blob", fields[2])
		if err != nil {
			return nil, err
		}
		if err := verifyRepositoryBlob(fields[2], blob); err != nil {
			return nil, err
		}
		return blob, nil
	}
	return nil, fmt.Errorf("git: empty repository path")
}
