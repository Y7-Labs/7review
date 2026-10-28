package skills

import (
	"strings"
	"testing"
)

func TestParsePolicyMethodRejectsAmbiguousFrontmatter(t *testing.T) {
	valid := "---\nname: backend\ndescription: Review backend.\nallowed-tools: diff-analyzer\n---\nCheck the backend invariant.\n"
	for _, tc := range []struct{ name, text string }{
		{"duplicate name", strings.Replace(valid, "name: backend", "name: backend\nname: backend", 1)},
		{"unknown field", strings.Replace(valid, "name: backend", "name: backend\nexecute: shell", 1)},
		{"duplicate metadata", strings.Replace(valid, "allowed-tools: diff-analyzer", "metadata:\n  owner: alice\n  owner: bob", 1)},
		{"invalid YAML", strings.Replace(valid, "description: Review backend.", "description: [unterminated", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(".7review/methods/backend/SKILL.md", []byte(tc.text)); err == nil {
				t.Fatal("ambiguous policy method was accepted")
			}
		})
	}
}

func TestParsePolicyMethodUsesYAMLAndLineDelimitedFrontmatter(t *testing.T) {
	data := "---\nname: backend\ndescription: 'Check --- without splitting this description.'\nallowed-tools: diff-analyzer\nmetadata:\n  review-domain: backend\n---\nKeep --- inside markdown body.\n"
	skill, err := Parse(".7review/methods/backend/SKILL.md", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Description != "Check --- without splitting this description." || skill.Body != "Keep --- inside markdown body." || skill.Metadata["review-domain"] != "backend" {
		t.Fatalf("structured method content was not preserved: %#v", skill)
	}
}
