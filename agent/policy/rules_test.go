package policy

import (
	"context"
	"strings"
	"testing"
)

const ruleJSON = `{"schema_version":1,"id":"AUTH-001","title":"Authorization","statement":"Authorize before mutation.","checks":["auth-check"]}`

func TestRuleDocumentsPreserveStatementAcrossFormats(t *testing.T) {
	for file, body := range map[string]string{
		"auth.json": ruleJSON,
		"auth.yaml": "schema_version: 1\nid: AUTH-001\ntitle: Authorization\nstatement: Authorize before mutation.\nchecks: [auth-check]\n",
		"auth.md":   "---\nschema_version: 1\nid: AUTH-001\ntitle: Authorization\nchecks: [auth-check]\n---\nAuthorize before mutation.\n",
	} {
		t.Run(file, func(t *testing.T) {
			c := validConfig()
			c.RuleRefs = []string{".7review/rules/" + file}
			r, err := ResolveReferences(context.Background(), c, func(context.Context, string) ([]byte, error) { return []byte(body), nil })
			if err != nil {
				t.Fatal(err)
			}
			e, err := Compile(r, CompileContext{})
			if err != nil {
				t.Fatal(err)
			}
			if len(e.Rules) != 1 || e.Rules[0].Statement != "Authorize before mutation." || !contains(e.QualityGate.RequiredCoverage, "auth-check") {
				t.Fatalf("rule was not executable: %#v", e)
			}
			if len(e.QualityGate.RuleIDs) != 0 {
				t.Fatal("selected rule narrowed the existing all-rules gate")
			}
		})
	}
}

func TestRuleReferenceRejectsInvalidOrAmbiguousAuthority(t *testing.T) {
	for _, body := range []string{
		strings.Replace(ruleJSON, `"schema_version":1`, `"schema_version":2`, 1),
		strings.Replace(ruleJSON, `"id":"AUTH-001"`, `"id":"AUTH-001","id":"AUTH-001"`, 1),
		strings.Replace(ruleJSON, `"title":"Authorization"`, `"title":"Authorization","execute":"shell"`, 1),
		strings.Replace(ruleJSON, `["auth-check"]`, `[]`, 1),
		strings.Replace(ruleJSON, `["auth-check"]`, `["auth-check","auth-check"]`, 1),
		strings.Replace(ruleJSON, `"statement":"Authorize before mutation."`, `"statement":""`, 1),
	} {
		c := validConfig()
		c.RuleRefs = []string{".7review/rules/auth.json"}
		if _, err := ResolveReferences(context.Background(), c, func(context.Context, string) ([]byte, error) { return []byte(body), nil }); err == nil {
			t.Fatalf("invalid rule accepted: %s", body)
		}
	}
	c := validConfig()
	c.RuleRefs = []string{".7review/rules/a.json", ".7review/rules/b.json"}
	if _, err := ResolveReferences(context.Background(), c, func(context.Context, string) ([]byte, error) { return []byte(ruleJSON), nil }); err == nil || !strings.Contains(err.Error(), "duplicate rule id") {
		t.Fatalf("duplicate rule identity silently aliased: %v", err)
	}
}

func TestUnresolvedRulesCannotBeSilentlyCompiled(t *testing.T) {
	c := validConfig()
	c.RuleRefs = []string{".7review/rules/auth.json"}
	if _, err := Compile(c, CompileContext{}); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("unresolved root rule was ignored: %v", err)
	}
	c.RuleRefs = nil
	c.Packs = []MethodPackV2{{ID: "auth", RuleRefs: []string{".7review/rules/auth.json"}, RiskFloor: "none", Publication: PublicationConstraintV2{Mode: "inherit"}}}
	if _, err := Compile(c, CompileContext{}); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("unresolved pack rule was ignored: %v", err)
	}
}

func TestSharedRuleIsLoadedOnceAndCannotMutateCompiledAuthority(t *testing.T) {
	c := validConfig()
	c.RuleRefs = []string{".7review/rules/auth.json"}
	c.Packs = []MethodPackV2{{ID: "auth", RuleRefs: append([]string(nil), c.RuleRefs...), RiskFloor: "none", Publication: PublicationConstraintV2{Mode: "inherit"}}}
	reads := 0
	r, err := ResolveReferences(context.Background(), c, func(context.Context, string) ([]byte, error) { reads++; return []byte(ruleJSON), nil })
	if err != nil {
		t.Fatal(err)
	}
	e, err := Compile(r, CompileContext{})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 || len(e.Rules) != 1 {
		t.Fatalf("shared rule duplicated: reads=%d rules=%d", reads, len(e.Rules))
	}
	e.Rules[0].Checks[0] = "mutated"
	next, err := Compile(r, CompileContext{})
	if err != nil {
		t.Fatal(err)
	}
	if next.Rules[0].Checks[0] != "auth-check" {
		t.Fatal("compiled result mutated reusable policy authority")
	}
}
