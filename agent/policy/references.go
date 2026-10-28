package policy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type DocumentSource struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type DocumentReader func(context.Context, string) ([]byte, error)

var documentPathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func validateDocumentRefs(field string, refs []string, directory string) error {
	if len(refs) > 256 {
		return fmt.Errorf("policy: %s exceeds maximum 256", field)
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if seen[ref] {
			return fmt.Errorf("policy: duplicate %s reference %q", field, ref)
		}
		seen[ref] = true
		if !documentPathPattern.MatchString(ref) || !strings.HasPrefix(ref, ".7review/"+directory+"/") || path.Clean(ref) != ref || strings.Contains(ref, "\\") {
			return fmt.Errorf("policy: %s path %q must stay inside .7review/%s", field, ref, directory)
		}
		for _, part := range strings.Split(ref, "/") {
			if part == ".." {
				return fmt.Errorf("policy: %s forbids parent traversal", field)
			}
		}
		switch path.Ext(ref) {
		case ".json", ".yaml", ".yml":
		case ".md":
			if directory != "rules" {
				return fmt.Errorf("policy: %s forbids markdown pack documents", field)
			}
		default:
			return fmt.Errorf("policy: %s unsupported document extension %q", field, path.Ext(ref))
		}
	}
	return nil
}

// ResolveReferences performs explicit bounded reads. The caller supplies a reader
// pinned to the same repository/base as the root, never a head/worktree fallback.
func ResolveReferences(ctx context.Context, config ReviewConfigV2, read DocumentReader) (ReviewConfigV2, error) {
	if err := Validate(config); err != nil {
		return ReviewConfigV2{}, err
	}
	resolved := config
	resolved.Packs = append([]MethodPackV2(nil), config.Packs...)
	resolved.documents = append([]DocumentSource(nil), config.documents...)
	total := 0
	for _, ref := range sortedCopy(config.PackRefs) {
		data, err := readReferencedDocument(ctx, read, ref, &total)
		if err != nil {
			return ReviewConfigV2{}, err
		}
		var document struct {
			SchemaVersion int          `json:"schema_version" yaml:"schema_version"`
			Pack          MethodPackV2 `json:"pack" yaml:"pack"`
		}
		raw, err := decodePolicyDocument(ref, data, &document)
		if err != nil {
			return ReviewConfigV2{}, err
		}
		if document.SchemaVersion != 1 {
			return ReviewConfigV2{}, fmt.Errorf("policy: pack document %s schema_version must be 1", ref)
		}
		if err := requireKeys(ref, raw, "schema_version", "pack"); err != nil {
			return ReviewConfigV2{}, err
		}
		if err := validateArrayObjects(map[string]any{"packs": []any{raw["pack"]}}, "packs", ref, []string{"id", "priority", "match", "methods", "checks", "risk_floor", "independent_review", "publication"}); err != nil {
			return ReviewConfigV2{}, err
		}
		sum := sha256.Sum256(data)
		resolved.documents = append(resolved.documents, DocumentSource{Kind: "pack", ID: document.Pack.ID, Path: ref, Digest: "sha256:" + hex.EncodeToString(sum[:])})
		resolved.Packs = append(resolved.Packs, document.Pack)
	}
	resolved.PackRefs = nil
	if err := Validate(resolved); err != nil {
		return ReviewConfigV2{}, err
	}
	resolved.defaultRuleRefs = stableUnion(config.defaultRuleRefs, config.RuleRefs)
	allRules := append([]string(nil), resolved.defaultRuleRefs...)
	for _, pack := range resolved.Packs {
		allRules = stableUnion(allRules, pack.RuleRefs)
	}
	if len(allRules) > 256 {
		return ReviewConfigV2{}, fmt.Errorf("policy: referenced rules exceed maximum 256")
	}
	resolved.rules = make(map[string]ResolvedRule, len(allRules))
	ids := map[string]string{}
	for ref, rule := range config.rules {
		resolved.rules[ref] = rule
		ids[rule.ID] = ref
	}
	for _, ref := range sortedCopy(allRules) {
		if _, ok := resolved.rules[ref]; ok {
			continue
		}
		data, err := readReferencedDocument(ctx, read, ref, &total)
		if err != nil {
			return ReviewConfigV2{}, err
		}
		rule, err := decodeRule(ref, data)
		if err != nil {
			return ReviewConfigV2{}, err
		}
		if old, ok := ids[rule.ID]; ok {
			return ReviewConfigV2{}, fmt.Errorf("policy: duplicate rule id %q in %s and %s", rule.ID, old, ref)
		}
		sum := sha256.Sum256(data)
		source := DocumentSource{Kind: "rule", ID: rule.ID, Path: ref, Digest: "sha256:" + hex.EncodeToString(sum[:])}
		resolved.rules[ref] = ResolvedRule{RuleV1: rule, Source: source}
		ids[rule.ID] = ref
		resolved.documents = append(resolved.documents, source)
	}
	resolved.RuleRefs = nil
	return resolved, nil
}

func readReferencedDocument(ctx context.Context, read DocumentReader, ref string, total *int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if read == nil {
		return nil, fmt.Errorf("policy: document reader is not configured")
	}
	data, err := read(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("policy: read policy document %s: %w", ref, err)
	}
	*total += len(data)
	if *total > MaxConfigBytes {
		return nil, fmt.Errorf("policy: referenced documents exceed %d bytes", MaxConfigBytes)
	}
	return data, nil
}

func decodePolicyDocument(filePath string, data []byte, target any) (map[string]any, error) {
	if len(data) > MaxConfigBytes {
		return nil, fmt.Errorf("policy: document %s exceeds size bound", filePath)
	}
	var raw map[string]any
	switch path.Ext(filePath) {
	case ".json":
		if err := rejectDuplicateJSONKeys(json.NewDecoder(bytes.NewReader(data)), filePath, nil); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			return nil, fmt.Errorf("policy: %s: %w", filePath, err)
		}
		if err := requireEOF(decoder); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
	case ".yaml", ".yml":
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(target); err != nil {
			return nil, fmt.Errorf("policy: %s: %w", filePath, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, fmt.Errorf("policy: %s requires one YAML document", filePath)
		}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("policy: unsupported document %s", filePath)
	}
	return raw, nil
}
