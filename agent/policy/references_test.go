package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func packDocumentFixture(t *testing.T) []byte {
	t.Helper()
	pack := MethodPackV2{ID: "external", Match: MatchV2{}, Methods: []string{"repo/backend"}, Checks: []string{"external-check"}, RiskFloor: "high", Publication: PublicationConstraintV2{Mode: "inherit", ArtifactClasses: []string{"assessment"}}}
	data, err := json.Marshal(struct {
		SchemaVersion int          `json:"schema_version"`
		Pack          MethodPackV2 `json:"pack"`
	}{1, pack})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPackReferencesCannotCompileWithoutResolution(t *testing.T) {
	c := validConfig()
	c.PackRefs = []string{".7review/packs/security.json"}
	if _, err := Compile(c, CompileContext{}); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("unresolved references were ignored: %v", err)
	}
}

func TestPackReferenceConfinementAndBoundsBeforeRead(t *testing.T) {
	for _, ref := range []string{"/tmp/pack.json", ".7review/packs/../../escape.json", ".7review/packs/sub/../security.json", "https://example.test/pack.json", ".7review/packs/a%2fb.json", ".7review/packs/a.txt", ".7review/rules/a.json"} {
		t.Run(ref, func(t *testing.T) {
			c := validConfig()
			c.PackRefs = []string{ref}
			_, err := ResolveReferences(context.Background(), c, func(context.Context, string) ([]byte, error) {
				t.Fatal("unsafe reference reached reader")
				return nil, nil
			})
			if err == nil {
				t.Fatal("unsafe reference accepted")
			}
		})
	}
}

func TestReferencedPackContentIsBoundAndInputIsUnchanged(t *testing.T) {
	c := validConfig()
	c.PackRefs = []string{".7review/packs/security.json"}
	before, _ := json.Marshal(c)
	data := packDocumentFixture(t)
	resolve := func(body []byte) EffectivePolicy {
		r, err := ResolveReferences(context.Background(), c, func(_ context.Context, path string) ([]byte, error) {
			if path != c.PackRefs[0] {
				t.Fatal(path)
			}
			return body, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		e, err := Compile(r, CompileContext{})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	first, second := resolve(data), resolve(append(append([]byte(nil), data...), '\n'))
	if first.Digest == second.Digest || len(first.Documents) != 1 || first.Documents[0].ID != "external" || !contains(first.RequiredChecks, "external-check") {
		t.Fatalf("referenced authority not bound: %#v", first)
	}
	after, _ := json.Marshal(c)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reference resolution mutated root input")
	}
}

func TestReferencedPackRejectsIncompleteOrOversizedDocuments(t *testing.T) {
	for _, body := range []string{`{"schema_version":1,"pack":{"id":"external"}}`, `{"schema_version":2,"pack":{}}`, strings.Repeat("x", MaxConfigBytes+1)} {
		c := validConfig()
		c.PackRefs = []string{".7review/packs/security.json"}
		if _, err := ResolveReferences(context.Background(), c, func(context.Context, string) ([]byte, error) { return []byte(body), nil }); err == nil {
			t.Fatal("invalid document accepted")
		}
	}
	c := validConfig()
	c.PackRefs = []string{".7review/packs/security.json"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveReferences(ctx, c, func(context.Context, string) ([]byte, error) { return nil, fmt.Errorf("reader must not run") })
	if err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}
