package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Y4NN777/7review/agent/policy"
)

type policyCommandOptions struct {
	file         string
	projectID    string
	paths        []string
	branch       string
	author       string
	labels       []string
	capabilities []string
}

type policyValidationOutput struct {
	Valid         bool   `json:"valid"`
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
}

func runPolicyCommand(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: 7review policy <validate|explain> --file <review.yaml>")
	}
	action := args[0]
	opts, err := parsePolicyCommandOptions(args[1:])
	if err != nil {
		return err
	}
	if opts.file == "" {
		return fmt.Errorf("policy: --file is required")
	}
	config, err := policy.Load(opts.file)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	switch action {
	case "validate":
		return encoder.Encode(policyValidationOutput{Valid: true, SchemaVersion: config.SchemaVersion, ProjectID: config.ProjectID, Packs: len(config.Packs), Delegations: len(config.Delegations)})
	case "explain":
		effective, err := policy.Compile(config, policy.CompileContext{
			ProjectID: opts.projectID, ChangedPaths: opts.paths, Branch: opts.branch,
			Author: opts.author, Labels: opts.labels, RuntimeAllowed: opts.capabilities,
		})
		if err != nil {
			return err
		}
		return encoder.Encode(policyExplanationOutput{
			TrustedSource: "unbound_preview", PolicyDigest: effective.Digest,
			Methods: effective.Methods, RequiredChecks: effective.RequiredChecks,
			RiskFloor: effective.RiskFloor, IndependentReview: effective.IndependentReview,
			Publication: effective.Publication, GateMode: effective.GateMode,
			AllowedCapabilities: effective.AllowedTools, MatchedPacks: effective.MatchedPacks,
			Decisions: effective.Explanation,
		})
	default:
		return fmt.Errorf("policy: unsupported command %q", action)
	}
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
