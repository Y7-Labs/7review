package policy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func inheritanceFixture(t *testing.T, links map[string][]string) ([]byte, ReviewConfigV2) {
	t.Helper()
	c := validConfig()
	c.Delegations = []DelegationV2{}
	c.Packs = []MethodPackV2{
		{ID: "parent", Priority: 900, Match: MatchV2{Paths: []string{"unmatched/**"}}, Methods: []string{"repo/auth"}, Checks: []string{"parent-check"}, RiskFloor: "high", IndependentReview: true, Publication: PublicationConstraintV2{Mode: "none", ArtifactClasses: []string{"assessment"}, ManualRequired: true}},
		{ID: "child", Priority: 1, Match: MatchV2{Paths: []string{"backend/**"}}, Methods: []string{"repo/child"}, Checks: []string{"child-check"}, RiskFloor: "low", Publication: PublicationConstraintV2{Mode: "setup_grant", ArtifactClasses: []string{"assessment", "comment"}}},
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	for _, value := range root["packs"].([]any) {
		pack := value.(map[string]any)
		if parents, ok := links[pack["id"].(string)]; ok {
			pack["inherits"] = parents
		}
	}
	data, err = json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return data, c
}

func TestInheritanceDiamondDoesNotDuplicateOrReopenPublication(t *testing.T) {
	c := validConfig()
	pack := func(id string, parents []string, classes []string) MethodPackV2 {
		return MethodPackV2{ID: id, Inherits: parents, Match: MatchV2{Paths: []string{"unmatched/**"}}, Methods: []string{"repo/" + id}, Checks: []string{id}, RiskFloor: "none", Publication: PublicationConstraintV2{Mode: "inherit", ArtifactClasses: classes}}
	}
	c.Packs = []MethodPackV2{
		pack("root", nil, nil), pack("left", []string{"root"}, []string{"assessment"}),
		pack("right", []string{"root"}, []string{"comment"}), pack("leaf", []string{"right", "left"}, nil),
	}
	c.Packs[3].Match = MatchV2{Paths: []string{"backend/**"}}
	e, err := Compile(c, CompileContext{ChangedPaths: []string{"backend/a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Methods) != 5 || len(e.Publication.ArtifactClasses) != 0 {
		t.Fatalf("diamond duplicated obligations or reopened empty publication intersection: %#v", e)
	}
	second, err := Compile(c, CompileContext{ChangedPaths: []string{"backend/a.go"}})
	if err != nil || !reflect.DeepEqual(e, second) {
		t.Fatalf("recompilation changed result: %v", err)
	}
}

func TestInheritanceDoesNotCopyDelegatedReplacementAuthority(t *testing.T) {
	c := validConfig()
	c.Delegations = []DelegationV2{{ID: "parent-owner", GrantorScope: ScopeRefV2{Kind: "project", ID: "org/repo"}, TargetScope: ScopeRefV2{Kind: "path", ID: "unmatched/**"}, ReplaceRuleIDs: []string{"builtin/correctness"}, ReplaceFields: []string{"methods"}, ProvenanceRef: "owner-record"}}
	c.Packs = []MethodPackV2{
		{ID: "parent", DelegationID: "parent-owner", Match: MatchV2{Paths: []string{"unmatched/**"}}, Methods: []string{"repo/parent"}, RiskFloor: "none", Publication: PublicationConstraintV2{Mode: "inherit"}},
		{ID: "child", Inherits: []string{"parent"}, Match: MatchV2{Paths: []string{"backend/**"}}, RiskFloor: "none", Publication: PublicationConstraintV2{Mode: "inherit"}},
	}
	e, err := Compile(c, CompileContext{ProjectID: "org/repo", ChangedPaths: []string{"backend/a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(e.Methods, "builtin/correctness") || !contains(e.Methods, "repo/parent") {
		t.Fatalf("child acquired ancestor replacement authority: %#v", e)
	}
}

func TestInheritanceAddsObligationsWithoutInheritingSelectionOrPrecedence(t *testing.T) {
	data, _ := inheritanceFixture(t, map[string][]string{"child": {"parent"}})
	c, err := DecodeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(c)
	ctx := CompileContext{ProjectID: "org/repo", ChangedPaths: []string{"backend/auth.go"}, RuntimeAllowed: []string{"repo.read"}}
	effective, err := Compile(c, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(effective.Methods, []string{"builtin/correctness", "repo/auth", "repo/child"}) || !reflect.DeepEqual(effective.MatchedPacks, []string{"child"}) {
		t.Fatalf("inheritance changed selection or omitted parent methods: %#v", effective)
	}
	if !contains(effective.RequiredChecks, "parent-check") || !contains(effective.RequiredChecks, "child-check") || effective.RiskFloor != "high" || !effective.IndependentReview || effective.Publication.Mode != "none" || !effective.Publication.ManualRequired || !reflect.DeepEqual(effective.Publication.ArtifactClasses, []string{"assessment"}) {
		t.Fatalf("child weakened inherited protections: %#v", effective)
	}
	ctx.ChangedPaths = []string{"frontend/view.go"}
	unrelated, err := Compile(c, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unrelated.MatchedPacks) != 0 || contains(unrelated.Methods, "repo/auth") {
		t.Fatalf("inheritance broadened applicability: %#v", unrelated)
	}
	after, _ := json.Marshal(c)
	if string(before) != string(after) {
		t.Fatal("inheritance/compiler mutated input policy")
	}
}

func TestInheritanceRejectsInvalidGraphBeforeMatching(t *testing.T) {
	for _, tc := range []struct {
		name   string
		links  map[string][]string
		reason string
	}{
		{"missing", map[string][]string{"child": {"absent"}}, "unknown parent"},
		{"self cycle", map[string][]string{"parent": {"parent"}}, "cycle"},
		{"indirect cycle", map[string][]string{"child": {"parent"}, "parent": {"child"}}, "cycle"},
		{"duplicate edge", map[string][]string{"child": {"parent", "parent"}}, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := inheritanceFixture(t, tc.links)
			_, err := DecodeJSON(data)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("expected graph rejection %q, got %v", tc.reason, err)
			}
		})
	}
}
