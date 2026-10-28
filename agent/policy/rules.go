package policy

import (
	"fmt"
	"path"
	"strings"
)

type RuleV1 struct {
	SchemaVersion int      `json:"schema_version" yaml:"schema_version"`
	ID            string   `json:"id" yaml:"id"`
	Title         string   `json:"title" yaml:"title"`
	Statement     string   `json:"statement" yaml:"statement"`
	Checks        []string `json:"checks" yaml:"checks"`
}

type ResolvedRule struct {
	RuleV1
	Source DocumentSource `json:"source"`
}

func decodeRule(filePath string, data []byte) (RuleV1, error) {
	var rule RuleV1
	var raw map[string]any
	var err error
	if path.Ext(filePath) == ".md" {
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		if len(lines) < 3 || lines[0] != "---" {
			return rule, fmt.Errorf("policy: rule %s requires YAML frontmatter", filePath)
		}
		end := 1
		for end < len(lines) && lines[end] != "---" {
			end++
		}
		if end == len(lines) {
			return rule, fmt.Errorf("policy: rule %s has no closing frontmatter delimiter", filePath)
		}
		var header struct {
			SchemaVersion int      `yaml:"schema_version"`
			ID            string   `yaml:"id"`
			Title         string   `yaml:"title"`
			Checks        []string `yaml:"checks"`
		}
		raw, err = decodePolicyDocument(filePath+".yaml", []byte(strings.Join(lines[1:end], "\n")), &header)
		rule = RuleV1{SchemaVersion: header.SchemaVersion, ID: header.ID, Title: header.Title, Checks: header.Checks, Statement: strings.TrimSpace(strings.Join(lines[end+1:], "\n"))}
	} else {
		raw, err = decodePolicyDocument(filePath, data, &rule)
	}
	if err != nil {
		return RuleV1{}, err
	}
	fields := []string{"schema_version", "id", "title", "checks"}
	if path.Ext(filePath) != ".md" {
		fields = append(fields, "statement")
	}
	if err := requireKeys(filePath, raw, fields...); err != nil {
		return RuleV1{}, err
	}
	if rule.SchemaVersion != 1 || !validID(rule.ID) || strings.TrimSpace(rule.Title) == "" || len(rule.Title) > 256 || strings.TrimSpace(rule.Statement) == "" {
		return RuleV1{}, fmt.Errorf("policy: rule %s requires version 1, valid id, title and nonempty statement", filePath)
	}
	if err := uniqueIDs(filePath+".checks", rule.Checks, 1, 128); err != nil {
		return RuleV1{}, err
	}
	return rule, nil
}

func requireResolvedRules(config ReviewConfigV2) error {
	if len(config.RuleRefs) > 0 {
		return fmt.Errorf("policy: unresolved rule references; resolve trusted documents before compilation")
	}
	refs := append([]string(nil), config.defaultRuleRefs...)
	for _, pack := range config.Packs {
		refs = stableUnion(refs, pack.RuleRefs)
	}
	for _, ref := range refs {
		if _, ok := config.rules[ref]; !ok {
			return fmt.Errorf("policy: unresolved rule reference %s", ref)
		}
	}
	return nil
}

func applyRule(e *EffectivePolicy, config ReviewConfigV2, ref string, priority int32) {
	for _, old := range e.Rules {
		if old.Source.Path == ref {
			return
		}
	}
	rule := config.rules[ref]
	rule.Checks = append([]string(nil), rule.Checks...)
	e.Rules = append(e.Rules, rule)
	e.RequiredChecks = stableUnion(e.RequiredChecks, rule.Checks)
	e.QualityGate.RequiredCoverage = stableUnion(e.QualityGate.RequiredCoverage, rule.Checks)
	// An empty root rule filter means all rules, not an empty allowlist. Adding a
	// rule must not narrow that existing gate into ignoring other violations.
	if len(config.QualityGate.RuleIDs) > 0 {
		e.QualityGate.RuleIDs = stableUnion(e.QualityGate.RuleIDs, []string{rule.ID})
	}
	e.Explanation = append(e.Explanation, ExplanationEntry{Field: "rule", Value: rule.ID, Source: ref, Reason: "selected trusted rule contributes statement and mandatory coverage", Precedence: priority})
}
