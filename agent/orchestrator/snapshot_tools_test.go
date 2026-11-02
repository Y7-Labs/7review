package orchestrator

import (
	"github.com/Y4NN777/7review/agent/review"
	"testing"
)

func TestSnapshotDefinitionsIntersectFrozenPolicyAndSkillsAndAreDetached(t *testing.T) {
	var source review.Source
	source.Policy.Mode = "enforce"
	source.Policy.Enforced = true
	source.SkillActivations = []review.SkillActivation{{Name: "reader", AllowedTools: []string{"scm-api"}}}
	if got := SnapshotToolDefinitions(source); len(got) != 0 {
		t.Fatal("skill widened a denied policy")
	}
	source.Policy.AllowedCapabilities = []string{"repo.read"}
	definitions := SnapshotToolDefinitions(source)
	if len(definitions) != 4 {
		t.Fatalf("surface intersection changed: %#v", definitions)
	}
	for _, d := range definitions {
		if review.SnapshotToolSurface(d.Name) != "scm-api" {
			t.Fatal("foreign surface advertised")
		}
	}
	definitions[0].InputSchema["additionalProperties"] = true
	if SnapshotToolDefinitions(source)[0].InputSchema["additionalProperties"] != false {
		t.Fatal("schema mutation widened future requests")
	}
	source.SkillActivations = nil
	if len(SnapshotToolDefinitions(source)) != 0 {
		t.Fatal("policy alone advertised tools")
	}
	if len(SnapshotToolSchemas()) != 6 {
		t.Fatal("closed snapshot vocabulary changed")
	}
}
