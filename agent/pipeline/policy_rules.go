package pipeline

import (
	"github.com/Y4NN777/7review/agent/policy"
	"github.com/Y4NN777/7review/agent/review"
)

func applyTrustedRuleEvidence(rc *review.Context, rules []policy.ResolvedRule) {
	if !rc.Source.Policy.Enforced {
		return
	}
	declared := map[string]bool{}
	for _, doc := range rc.Source.Policy.Documents {
		if doc.Kind == "rule" {
			declared[doc.Path] = true
		}
	}
	// Corpus retrieval may observe proposed head content at the same path. It
	// must not supply authority for declared rules, even when the pack is inactive.
	sections := make([]review.Section, 0, len(rc.CorpusSections)+len(rules))
	for _, section := range rc.CorpusSections {
		if !declared[section.Path] {
			sections = append(sections, section)
		}
	}
	evidence := make([]review.EvidenceItem, 0, len(rc.Source.Evidence)+len(rules))
	for _, item := range rc.Source.Evidence {
		if !declared[item.Source] {
			evidence = append(evidence, item)
		}
	}
	for _, rule := range rules {
		content := "# " + rule.Title + "\n\n" + rule.Statement
		sections = append(sections, review.Section{Path: rule.Source.Path, Title: rule.ID, Content: content, Kind: review.KindRules})
		evidence = append(evidence, review.EvidenceItem{Source: rule.Source.Path, HeadingOrKey: rule.ID, Kind: review.KindRules, Authority: "rules", AuthorityLevel: "sot", CanJustifyFinding: true, ContentBytes: len(content), SelectionReason: "trusted rule at base " + rc.Source.Policy.SourceRevision + " digest " + rule.Source.Digest})
	}
	rc.CorpusSections, rc.Source.Evidence = sections, evidence
}
