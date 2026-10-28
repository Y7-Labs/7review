package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Y4NN777/7review/agent/policy"
)

func policyMethodPreviewFixture(t *testing.T, id string) (string, string) {
	t.Helper()
	file := writePolicyFixture(t)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var config policy.ReviewConfigV2
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config.Packs = []policy.MethodPackV2{{ID: "inactive", Match: policy.MatchV2{Paths: []string{"unmatched/**"}}, Methods: []string{id}, Checks: []string{}, RiskFloor: "none", Publication: policy.PublicationConstraintV2{Mode: "inherit", ArtifactClasses: []string{}}}}
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file, filepath.Dir(file)
}

func TestPolicyCLIRejectsMissingOrUnknownUnmatchedMethod(t *testing.T) {
	for _, id := range []string{"repo/backend", "builtin/unknown"} {
		t.Run(id, func(t *testing.T) {
			file, root := policyMethodPreviewFixture(t, id)
			var out bytes.Buffer
			err := runPolicyCommand([]string{"validate", "--file", file, "--repository-root", root}, &out)
			if err == nil || !strings.Contains(err.Error(), id) || out.Len() != 0 {
				t.Fatalf("invalid inactive method falsely validated: output=%s err=%v", out.String(), err)
			}
		})
	}
}

const localPreviewMethod = "---\nname: backend\ndescription: Local preview method.\nrequired-checks: backend-only\n---\nLOCAL_INACTIVE_METHOD_SENTINEL\n"

func TestPolicyCLIValidatesInactiveMethodWithoutActivatingIt(t *testing.T) {
	file, root := policyMethodPreviewFixture(t, "repo/backend")
	methodPath := filepath.Join(root, ".7review/methods/backend/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(methodPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(methodPath, []byte(localPreviewMethod), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runPolicyCommand([]string{"explain", "--file", file, "--repository-root", root, "--path", "other.go"}, &out); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	methods := result["methods"].([]any)
	if len(methods) != 1 || methods[0] != "builtin/correctness" || result["method_registry"] == nil || strings.Contains(out.String(), "backend-only") || strings.Contains(out.String(), "LOCAL_INACTIVE_METHOD_SENTINEL") {
		t.Fatalf("inactive method was unvalidated or activated: %s", out.String())
	}
	out.Reset()
	if err := runPolicyCommand([]string{"validate", "--file", file, "--repository-root", root}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"scope": "unbound_policy_bundle"`) || !strings.Contains(out.String(), `"methods": 2`) {
		t.Fatalf("validation claimed an ambiguous readiness scope: %s", out.String())
	}
}

func TestPolicyCLIRejectsEvenInsideRootMethodSymlink(t *testing.T) {
	file, root := policyMethodPreviewFixture(t, "repo/backend")
	methodPath := filepath.Join(root, ".7review/methods/backend/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(methodPath), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "method-data.md")
	if err := os.WriteFile(target, []byte(localPreviewMethod), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, methodPath); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runPolicyCommand([]string{"validate", "--file", file, "--repository-root", root}, &out); err == nil || out.Len() != 0 {
		t.Fatalf("method symlink falsely qualified: output=%s err=%v", out.String(), err)
	}
}

func TestPolicyCLIRejectsMethodDirectorySymlinkAndMissingRoot(t *testing.T) {
	file, root := policyMethodPreviewFixture(t, "repo/backend")
	var out bytes.Buffer
	if err := runPolicyCommand([]string{"validate", "--file", file}, &out); err == nil || !strings.Contains(err.Error(), "--repository-root") || out.Len() != 0 {
		t.Fatalf("implicit method root accepted: output=%s err=%v", out.String(), err)
	}
	target := filepath.Join(root, "regular-backend")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte(localPreviewMethod), 0600); err != nil {
		t.Fatal(err)
	}
	methods := filepath.Join(root, ".7review/methods")
	if err := os.MkdirAll(methods, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(methods, "backend")); err != nil {
		t.Fatal(err)
	}
	if err := runPolicyCommand([]string{"validate", "--file", file, "--repository-root", root}, &out); err == nil || out.Len() != 0 {
		t.Fatalf("method directory symlink accepted: output=%s err=%v", out.String(), err)
	}
}

func TestDocumentedPolicyExampleHasCompleteMethodRegistry(t *testing.T) {
	var out bytes.Buffer
	if err := runPolicyCommand([]string{"validate", "--file", "../../profiles/review.v2.example.yaml", "--repository-root", "../../examples/policy"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"valid": true`) || !strings.Contains(out.String(), `"methods": 2`) {
		t.Fatalf("documented example is incomplete: %s", out.String())
	}
}
