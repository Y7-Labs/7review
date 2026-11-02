package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Y4NN777/7review/agent/review"
)

func snapshotToolContext() *review.Context {
	rc := review.NewContext(review.Request{Provider: "github", ProjectID: "org/repo", ChangeID: "7"})
	rc.Source.Run.ID = "active-run"
	rc.Source.Policy = review.PolicyProjection{Mode: "enforce", Enforced: true, AllowedCapabilities: []string{"repo.read"}}
	rc.Source.SkillActivations = []review.SkillActivation{{Name: "snapshot-reader", AllowedTools: []string{"scm-api", "diff-analyzer", "corpus-selector"}}}
	rc.Source.SCM = &review.SCMContext{Provider: "github", ProjectID: "org/repo", ChangeID: "7"}
	rc.Source.ChangedFiles = []review.ChangedFile{{NewPath: "service.go", Patch: "+change", Status: "modified"}}
	return rc
}

func TestSnapshotToolRejectsForeignOrUnsupportedInputs(t *testing.T) {
	for _, input := range []map[string]any{{"run": "foreign-run"}, {"run": 7}, {"run": ""}, {"path": "foreign.go"}, {"run": "active-run", "extra": true}} {
		t.Run(string(mustToolJSON(t, input)), func(t *testing.T) {
			observation := (&Pipeline{}).executeReviewToolRequest(snapshotToolContext(), review.ToolRequest{Name: "get_changed_files", Input: input, Round: 1})
			if observation.Status != "denied" || observation.Result != "" {
				t.Fatalf("invalid arguments became successful active-run evidence: %#v", observation)
			}
		})
	}
	observation := (&Pipeline{}).executeReviewToolRequest(snapshotToolContext(), review.ToolRequest{Name: "get_changed_files", Input: map[string]any{"run": "active-run"}, Round: 1})
	if observation.Status != "ok" || observation.Round != 1 || !strings.Contains(observation.Result, "service.go") {
		t.Fatalf("valid snapshot request failed: %#v", observation)
	}
}

func mustToolJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSnapshotToolMarksTruncationWithoutBreakingJSONOrUTF8(t *testing.T) {
	rc := snapshotToolContext()
	rc.Source.SCM.Description = strings.Repeat("\u754c", 5000)
	observation := (&Pipeline{}).executeReviewToolRequest(rc, review.ToolRequest{Name: "get_merge_request", Round: 1})
	if observation.Status != "truncated" || !utf8.ValidString(observation.Result) || !json.Valid([]byte(observation.Result)) || len(observation.Result) > 4096 || observation.Reason == "" {
		t.Fatalf("incomplete tool output masqueraded as complete evidence: status=%s bytes=%d utf8=%v json=%v", observation.Status, len(observation.Result), utf8.ValidString(observation.Result), json.Valid([]byte(observation.Result)))
	}
}
