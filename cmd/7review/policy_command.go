package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

type policyCommandOptions struct {
	file           string
	projectID      string
	paths          []string
	branch         string
	author         string
	labels         []string
	capabilities   []string
	repositoryRoot string
}

type policyValidationOutput struct {
	Valid         bool   `json:"valid"`
	Scope         string `json:"scope"`
	Methods       int    `json:"methods"`
	SchemaVersion int    `json:"schema_version"`
	ProjectID     string `json:"project_id,omitempty"`
	Packs         int    `json:"packs"`
	Delegations   int    `json:"delegations"`
}

type policyExplanationOutput struct {
	TrustedSource       string                         `json:"trusted_source"`
	PolicyDigest        string                         `json:"policy_digest"`
	Methods             []string                       `json:"methods"`
	RequiredChecks      []string                       `json:"required_checks"`
	RiskFloor           string                         `json:"risk_floor"`
	IndependentReview   bool                           `json:"independent_review"`
	Publication         policy.PublicationConstraintV2 `json:"publication"`
	GateMode            string                         `json:"gate_mode"`
	AllowedCapabilities []string                       `json:"allowed_capabilities"`
	MatchedPacks        []string                       `json:"matched_packs"`
	Decisions           []policy.ExplanationEntry      `json:"decisions"`
	Documents           []policy.DocumentSource        `json:"documents,omitempty"`
	Rules               []policy.ResolvedRule          `json:"rules,omitempty"`
	MethodRegistry      []review.PolicyMethod          `json:"method_registry"`
}

func runPolicyCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: 7review policy <validate|explain> --file <review.yaml>")
	}
	action := args[0]
	if action != "validate" && action != "explain" {
		return fmt.Errorf("policy: unsupported command %q", action)
	}
	opts, err := parsePolicyCommandOptions(args[1:])
	if err != nil {
		return err
	}
	var root *os.Root
	defer func() {
		if root != nil {
			root.Close()
		}
	}()
	read := func(ctx context.Context, path string) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if root == nil {
			if opts.repositoryRoot == "" {
				return nil, fmt.Errorf("policy: referenced documents/methods require --repository-root for unbound local preview")
			}
			var err error
			root, err = os.OpenRoot(opts.repositoryRoot)
			if err != nil {
				return nil, err
			}
		}
		return readPolicyPreviewFile(root, path)
	}
	if opts.file == "" {
		return fmt.Errorf("policy: --file is required")
	}
	config, err := policy.Load(opts.file)
	if err != nil {
		return err
	}
	hasReferences := len(config.PackRefs) > 0 || len(config.RuleRefs) > 0
	for _, pack := range config.Packs {
		hasReferences = hasReferences || len(pack.RuleRefs) > 0
	}
	if hasReferences {
		config, err = policy.ResolveReferences(context.Background(), config, read)
		if err != nil {
			return err
		}
	}
	effective, err := policy.Compile(config, policy.CompileContext{
		ProjectID: opts.projectID, ChangedPaths: opts.paths, Branch: opts.branch,
		Author: opts.author, Labels: opts.labels, RuntimeAllowed: opts.capabilities,
	})
	if err != nil {
		return err
	}
	_, registry, err := pipeline.PreviewPolicyMethods(context.Background(), config, &effective, read)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	switch action {
	case "validate":
		return encoder.Encode(policyValidationOutput{Valid: true, Scope: "unbound_policy_bundle", Methods: len(registry), SchemaVersion: config.SchemaVersion, ProjectID: config.ProjectID, Packs: len(config.Packs), Delegations: len(config.Delegations)})
	case "explain":
		return encoder.Encode(policyExplanationOutput{
			MethodRegistry: registry,
			TrustedSource:  "unbound_preview", PolicyDigest: effective.Digest,
			Methods: effective.Methods, RequiredChecks: effective.RequiredChecks,
			RiskFloor: effective.RiskFloor, IndependentReview: effective.IndependentReview,
			Publication: effective.Publication, GateMode: effective.GateMode,
			AllowedCapabilities: effective.AllowedTools, MatchedPacks: effective.MatchedPacks,
			Decisions: effective.Explanation,
			Documents: effective.Documents,
			Rules:     effective.Rules,
		})
	default:
		return fmt.Errorf("policy: unsupported command %q", action)
	}
}

func readPolicyPreviewFile(root *os.Root, path string) ([]byte, error) {
	parts := strings.Split(path, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(prefix)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("policy: %s must be a regular file under regular directories", path)
		}
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("policy: %s is not a regular file", path)
	}
	return io.ReadAll(io.LimitReader(file, policy.MaxConfigBytes+1))
}

func parsePolicyCommandOptions(args []string) (policyCommandOptions, error) {
	var opts policyCommandOptions
	for i := 0; i < len(args); i++ {
		name, inline, hasInline := strings.Cut(args[i], "=")
		value := inline
		if !hasInline {
			if i+1 >= len(args) {
				return opts, fmt.Errorf("policy: %s requires a value", name)
			}
			i++
			value = args[i]
		}
		switch name {
		case "--file":
			opts.file = value
		case "--repository-root":
			opts.repositoryRoot = value
		case "--project":
			opts.projectID = value
		case "--path":
			opts.paths = append(opts.paths, value)
		case "--branch":
			opts.branch = value
		case "--author":
			opts.author = value
		case "--label":
			opts.labels = append(opts.labels, value)
		case "--capability":
			opts.capabilities = append(opts.capabilities, value)
		default:
			return opts, fmt.Errorf("policy: unknown flag %s", name)
		}
	}
	sort.Strings(opts.paths)
	sort.Strings(opts.labels)
	sort.Strings(opts.capabilities)
	return opts, nil
}
