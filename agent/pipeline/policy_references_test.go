package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/policy"
)

const referencedPack = `{"schema_version":1,"pack":{"id":"base-security","priority":900,"match":{"paths":["unmatched/**"]},"methods":["repo/backend"],"checks":["inherited-auth-check"],"risk_floor":"high","independent_review":false,"publication":{"mode":"inherit","artifact_classes":["assessment","check"],"manual_required":true}}}`

func packReferenceFixture(t *testing.T, document string) (SCMPolicyAdmission, *methodFileReader) {
	t.Helper()
	a, reader := methodAdmissionFixture(t, "builtin/correctness", backendMethod)
	c, err := policy.Decode("review.yaml", []byte(reader.files[".7review/review.yaml"]))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(c)
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	root["pack_refs"] = []string{".7review/packs/security.json"}
	root["packs"].([]any)[0].(map[string]any)["inherits"] = []string{"base-security"}
	data, _ = json.Marshal(root)
	reader.files[".7review/review.yaml"] = string(data)
	if document != "" {
		reader.files[".7review/packs/security.json"] = document
	}
	return a, reader
}

func TestReferencedPackInheritanceControlsExecutionFromBase(t *testing.T) {
	req, scm := trustedPolicyFixture()
	a, reader := packReferenceFixture(t, referencedPack)
	model := &methodPromptProvider{}
	store := NewMemoryRunStore()
	p := policyExecutionPipeline(store, model, scm, TrustedPolicyResult{})
	p.TrustedPolicy = a
	if err := p.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) == 0 || !strings.Contains(model.prompts[0], "BASE_METHOD_SENTINEL") || !strings.Contains(model.prompts[0], "inherited-auth-check") {
		t.Fatalf("referenced parent obligations did not reach execution: prompts=%q reads=%v", model.prompts, reader.reads)
	}
	run, err := store.Get(context.Background(), "owner/repository!7")
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Source.Policy.Documents) != 1 || run.Source.Policy.Documents[0].Revision != "base-sha" || run.Source.Policy.Documents[0].ID != "base-security" {
		t.Fatalf("base document provenance lost: %#v", run.Source.Policy.Documents)
	}
}

func TestReferencedPackFailureCannotFallThroughToModel(t *testing.T) {
	for _, tc := range []struct{ name, document, reason string }{
		{"missing", "", "read policy document"},
		{"unknown field", strings.Replace(referencedPack, `"schema_version":1`, `"schema_version":1,"execute":"shell"`, 1), "unknown field"},
		{"duplicate key", strings.Replace(referencedPack, `"id":"base-security"`, `"id":"base-security","id":"base-security"`, 1), "duplicate JSON key"},
		{"duplicate stable ID", strings.Replace(referencedPack, `"id":"base-security"`, `"id":"backend"`, 1), "duplicate pack id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, scm := trustedPolicyFixture()
			a, _ := packReferenceFixture(t, tc.document)
			model := &methodPromptProvider{}
			p := policyExecutionPipeline(NewMemoryRunStore(), model, scm, TrustedPolicyResult{})
			p.TrustedPolicy = a
			if err := p.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), tc.reason) || len(model.prompts) != 0 {
				t.Fatalf("invalid reference crossed model boundary: err=%v calls=%d", err, len(model.prompts))
			}
		})
	}
}
