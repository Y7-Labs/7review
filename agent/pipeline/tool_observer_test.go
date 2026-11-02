package pipeline

import (
	"context"
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

func TestToolRequestBoundsCannotSilentlyOmitProposals(t *testing.T) {
	for _, fault := range []string{"too_many", "blank_name", "long_name", "long_reason", "oversized_input"} {
		t.Run(fault, func(t *testing.T) {
			requests := []review.ToolRequest{{Name: "get_changed_files"}}
			switch fault {
			case "too_many":
				for len(requests) < 6 {
					requests = append(requests, review.ToolRequest{Name: "get_diff_summary"})
				}
			case "blank_name":
				requests = append(requests, review.ToolRequest{Name: "   "})
			case "long_name":
				requests[0].Name = strings.Repeat("x", review.MaxModelToolNameBytes+1)
			case "long_reason":
				requests[0].Reason = strings.Repeat("x", review.MaxModelToolReasonBytes+1)
			case "oversized_input":
				requests[0].Input = map[string]any{"run": strings.Repeat("x", review.MaxModelToolInputBytes)}
			}
			raw := mustToolJSON(t, map[string]any{"tool_requests": requests, "findings": []review.Finding{}})
			_, _, parsed, status := parseReviewOutputDetailed(string(raw))
			if status != "tool_loop_incomplete" || len(parsed) != 0 {
				t.Fatalf("invalid proposals were silently dropped: status=%s requests=%d", status, len(parsed))
			}
			_, _, parsed, status = parseReviewResponses([]string{string(raw)})
			if status != "tool_loop_incomplete" || len(parsed) != 0 {
				t.Fatalf("batch parser erased invalid tool plan: %s %#v", status, parsed)
			}
			results := recordedReducerFixture(t, 1)
			results[0].Outcome.Response = raw
			results[0].Outcome.ResponseDigest = modelDigest(raw)
			reduced, err := (ModelReducerV1{}).ReduceModelResults(context.Background(), results)
			if err != nil || reduced.State != review.ModelReductionBlocked || len(reduced.ToolRequests) != 0 || reduced.Source.Report.Draft != "" {
				t.Fatalf("invalid tool plan became successful evidence: %#v %v", reduced, err)
			}
		})
	}
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

func TestSnapshotToolDoesNotFabricateEmptyMissingData(t *testing.T) {
	for _, name := range []string{"get_changed_files", "get_diff_summary"} {
		t.Run(name, func(t *testing.T) {
			rc := snapshotToolContext()
			if name == "get_changed_files" {
				rc.Source.ChangedFiles = nil
				rc.Source.SCM = nil
				rc.Source.Diff = &review.StructuredDiff{Files: []review.FileDiff{{Path: "service.go", Patch: "+change"}}}
			}
			observation := (&Pipeline{}).executeReviewToolRequest(rc, review.ToolRequest{Name: name, Round: 1})
			if observation.Status != "error" || observation.Result != "" {
				t.Fatalf("missing data became successful empty evidence: %#v", observation)
			}
		})
	}
	missing := snapshotToolContext()
	missing.Source.ChangedFiles = nil
	missing.Source.SCM = nil
	missing.Source.Diff = nil
	if observation := (&Pipeline{}).executeReviewToolRequest(missing, review.ToolRequest{Name: "get_changed_files"}); observation.Status != "error" || observation.Result != "" {
		t.Fatalf("absent snapshot became an empty file list: %#v", observation)
	}
	empty := snapshotToolContext()
	empty.Source.ChangedFiles = nil
	empty.Source.Diff = &review.StructuredDiff{}
	for _, name := range []string{"get_changed_files", "get_diff_summary"} {
		if observation := (&Pipeline{}).executeReviewToolRequest(empty, review.ToolRequest{Name: name}); observation.Status != "ok" || !json.Valid([]byte(observation.Result)) {
			t.Fatalf("known empty snapshot was confused with missing data: %#v", observation)
		}
	}
}
