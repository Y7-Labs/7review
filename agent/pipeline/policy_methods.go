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
const maxDeclaredPolicyMethods = 256

type ResolvedPolicyMethod struct {
	Provenance review.PolicyMethod
	Section    review.Section
	Activation review.SkillActivation
}

// PreviewPolicyMethods qualifies the same method registry as service admission
// without claiming an attested revision or authorizing any execution.
func PreviewPolicyMethods(ctx context.Context, config policy.ReviewConfigV2, effective *policy.EffectivePolicy, read func(context.Context, string) ([]byte, error)) ([]ResolvedPolicyMethod, []review.PolicyMethod, error) {
	return resolvePolicyMethodRegistry(ctx, effective, policy.Source{Revision: "unbound_preview"}, declaredPolicyMethods(config), read)
}

func resolvePolicyMethods(ctx context.Context, effective *policy.EffectivePolicy, source policy.Source, read func(context.Context, string) ([]byte, error)) ([]ResolvedPolicyMethod, error) {
	selected, _, err := resolvePolicyMethodRegistry(ctx, effective, source, effective.Methods, read)
	return selected, err
}

func declaredPolicyMethods(config policy.ReviewConfigV2) []string {
	methods := append([]string(nil), config.Defaults.Methods...)
	for _, pack := range config.Packs {
		methods = append(methods, pack.Methods...)
	}
	return methods
}

func resolvePolicyMethodRegistry(ctx context.Context, effective *policy.EffectivePolicy, source policy.Source, declared []string, read func(context.Context, string) ([]byte, error)) ([]ResolvedPolicyMethod, []review.PolicyMethod, error) {
	ids := append(append([]string(nil), declared...), effective.Methods...)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) > maxDeclaredPolicyMethods {
		return nil, nil, fmt.Errorf("policy: declared methods exceed maximum %d", maxDeclaredPolicyMethods)
	}
	registry := make(map[string]ResolvedPolicyMethod, len(ids))
	provenance := make([]review.PolicyMethod, 0, len(ids))
	totalBytes := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		namespace, name, ok := strings.Cut(id, "/")
		if !ok || !policyMethodName.MatchString(name) {
			return nil, nil, fmt.Errorf("policy: invalid namespaced method ID %q", id)
		}
		var data []byte
		var methodPath, revision string
		switch namespace {
		case "builtin":
			if name != "correctness" {
				return nil, nil, fmt.Errorf("policy: unknown builtin method %q", id)
			}
			methodPath, revision, data = "methods/correctness/SKILL.md", "builtin", builtinCorrectness
		case "repo":
			methodPath = ".7review/methods/" + name + "/SKILL.md"
			revision = source.Revision
			var err error
			if read == nil {
				return nil, nil, fmt.Errorf("policy: method %q reader is not configured", id)
			}
			data, err = read(ctx, methodPath)
			if err != nil {
				return nil, nil, fmt.Errorf("policy: read method %q at base %s: %w", id, revision, err)
			}
		default:
			return nil, nil, fmt.Errorf("policy: unsupported method namespace for %q", id)
		}
		totalBytes += len(data)
		if totalBytes > maxPolicyMethodBytes {
			return nil, nil, fmt.Errorf("policy: declared method content exceeds %d bytes", maxPolicyMethodBytes)
		}
		skill, err := skills.Parse(methodPath, data)
		if err != nil {
			return nil, nil, fmt.Errorf("policy: method %q: %w", id, err)
		}
		sum := sha256.Sum256(data)
		meta := review.PolicyMethod{ID: id, Path: methodPath, Revision: revision, Digest: "sha256:" + hex.EncodeToString(sum[:])}
		activation := skill.Activation("selected by trusted effective policy")
		activation.Name, activation.Category, activation.Required = id, "policy", true
		if err := policy.ValidateMethodChecks(id, activation.RequiredChecks); err != nil {
			return nil, nil, err
		}
		registry[id] = ResolvedPolicyMethod{
			Provenance: meta, Activation: activation,
			Section: review.Section{Path: methodPath, Title: id, Content: skill.ActivationContent(), Kind: review.KindRules},
		}
		provenance = append(provenance, meta)
	}
	resolved := make([]ResolvedPolicyMethod, 0, len(effective.Methods))
	for _, id := range effective.Methods {
		method := registry[id]
		activation := method.Activation
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
		resolved = append(resolved, method)
	}
	// Include runtime-owned builtins as well as base-owned content in policy
	// identity, so changing instructions cannot reuse an old execution identity.
	canonical, err := json.Marshal(struct {
		Parent  string
		Methods []review.PolicyMethod
	}{effective.Digest, provenance})
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(canonical)
	effective.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return resolved, provenance, nil
}
