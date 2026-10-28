package pipeline

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
	"github.com/Y4NN777/7review/agent/skills"
)

//go:embed methods/correctness/SKILL.md
var builtinCorrectness []byte

var policyMethodName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}[a-z0-9]$|^[a-z0-9]$`)

const maxPolicyMethodBytes = 1 << 20

type ResolvedPolicyMethod struct {
	Provenance review.PolicyMethod
	Section    review.Section
	Activation review.SkillActivation
}

func resolvePolicyMethods(ctx context.Context, effective *policy.EffectivePolicy, source policy.Source, read func(context.Context, string) ([]byte, error)) ([]ResolvedPolicyMethod, error) {
	resolved := make([]ResolvedPolicyMethod, 0, len(effective.Methods))
	provenance := make([]review.PolicyMethod, 0, len(effective.Methods))
	totalBytes := 0
	for _, id := range effective.Methods {
		namespace, name, ok := strings.Cut(id, "/")
		if !ok || !policyMethodName.MatchString(name) {
			return nil, fmt.Errorf("policy: invalid namespaced method ID %q", id)
		}
		var data []byte
		var methodPath, revision string
		switch namespace {
		case "builtin":
			if name != "correctness" {
				return nil, fmt.Errorf("policy: unknown builtin method %q", id)
			}
			methodPath, revision, data = "methods/correctness/SKILL.md", "builtin", builtinCorrectness
		case "repo":
			methodPath = ".7review/methods/" + name + "/SKILL.md"
			revision = source.Revision
			var err error
			data, err = read(ctx, methodPath)
			if err != nil {
				return nil, fmt.Errorf("policy: read method %q at base %s: %w", id, revision, err)
			}
		default:
			return nil, fmt.Errorf("policy: unsupported method namespace %q", namespace)
		}
		totalBytes += len(data)
		if totalBytes > maxPolicyMethodBytes {
			return nil, fmt.Errorf("policy: selected method content exceeds %d bytes", maxPolicyMethodBytes)
		}
		skill, err := skills.Parse(methodPath, data)
		if err != nil {
			return nil, fmt.Errorf("policy: method %q: %w", id, err)
		}
		sum := sha256.Sum256(data)
		meta := review.PolicyMethod{ID: id, Path: methodPath, Revision: revision, Digest: "sha256:" + hex.EncodeToString(sum[:])}
		activation := skill.Activation("selected by trusted effective policy")
		activation.Name, activation.Category, activation.Required = id, "policy", true
		for _, check := range activation.RequiredChecks {
			if !slices.Contains(effective.RequiredChecks, check) {
				effective.RequiredChecks = append(effective.RequiredChecks, check)
			}
			if !slices.Contains(effective.QualityGate.RequiredCoverage, check) {
				effective.QualityGate.RequiredCoverage = append(effective.QualityGate.RequiredCoverage, check)
			}
		}
		if len(activation.RequiredChecks) > 0 {
			effective.Explanation = append(effective.Explanation, policy.ExplanationEntry{
				Field: "required_checks", Value: strings.Join(activation.RequiredChecks, ","), Source: id,
				Reason: "required checks declared by selected trusted method",
			})
		}
		resolved = append(resolved, ResolvedPolicyMethod{
			Provenance: meta, Activation: activation,
			Section: review.Section{Path: methodPath, Title: id, Content: skill.ActivationContent(), Kind: review.KindRules},
		})
		provenance = append(provenance, meta)
	}
	// Include runtime-owned builtins as well as base-owned content in policy
	// identity, so changing instructions cannot reuse an old execution identity.
	canonical, err := json.Marshal(struct {
		Parent  string
		Methods []review.PolicyMethod
	}{effective.Digest, provenance})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	effective.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return resolved, nil
}
