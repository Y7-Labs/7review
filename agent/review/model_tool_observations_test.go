package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func snapshotUsageFixture(t *testing.T) (ModelReduction, ModelToolObservations) {
	t.Helper()
	parent := ModelReduction{Version: 1, State: ModelReductionNeedsTools,
		GroupDigest: snapshotTestDigest([]byte("group")), InputDigest: snapshotTestDigest([]byte("inputs")),
		Cursor: ModelCursor{Phase: ModelReviewBatches, BatchCount: 1}, NextCursor: &ModelCursor{Phase: ModelToolRound, Round: 1},
		ToolRequests: []ToolRequest{{Name: "get_changed_files", Round: 1}},
	}
	parent.Source.Run.ID = "run"
	parent.Source.Attempt.AttemptID = "attempt"
	parent.Source.Policy.AllowedCapabilities = []string{"repo.read"}
	parent.Source.Policy.Enforced = true
	parent.Source.SkillActivations = []SkillActivation{{Name: "reader", AllowedTools: []string{"scm-api"}}}
	requests, err := json.Marshal(parent.ToolRequests)
	if err != nil {
		t.Fatal(err)
	}
	artifact := ModelToolObservations{Version: 1, RunID: "run", AttemptID: "attempt",
		ParentGroupDigest: parent.GroupDigest, ParentInputDigest: parent.InputDigest,
		RequestDigest: snapshotTestDigest(requests), Requests: parent.ToolRequests,
		Observations: []ToolObservation{{Name: "get_changed_files", Round: 1, Surface: "scm-api", Status: "ok", Result: `{"files":[]}`}},
	}
	return parent, artifact
}

func snapshotTestDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func TestSnapshotToolSurfaceIsClosedAndExact(t *testing.T) {
	for name, want := range map[string]string{
		"get_merge_request": "scm-api", "get_changed_files": "scm-api",
		"list_discussions": "scm-api", "get_inline_positions": "scm-api",
		"get_diff_summary": "diff-analyzer", "get_selected_context": "corpus-selector",
		"": "", "shell": "", "publish_final": "", " get_changed_files ": "",
	} {
		if got := SnapshotToolSurface(name); got != want {
			t.Fatalf("surface for %q: got %q want %q", name, got, want)
		}
	}
}

func TestSnapshotObservationUsageCountsEveryEvaluationAndEncodedBytes(t *testing.T) {
	for _, status := range []string{"ok", "denied", "error", "truncated"} {
		t.Run(status, func(t *testing.T) {
			parent, artifact := snapshotUsageFixture(t)
			o := &artifact.Observations[0]
			o.Status = status
			if status != "ok" {
				o.Reason = "bounded outcome"
				o.Result = ""
			}
			if status == "truncated" {
				o.Result = `{"truncated":true,"original_bytes":5000,"preview":"partial"}`
			}
			raw, err := json.Marshal(o)
			if err != nil {
				t.Fatal(err)
			}
			usage, err := artifact.SnapshotUsage(parent)
			want := ResourceLimitScope{ToolCalls: 1, ObservationBytes: int64(len(raw))}
			if err != nil || usage != want {
				t.Fatalf("status %s gained free evaluation or fake provider usage: %#v want %#v: %v", status, usage, want, err)
			}
		})
	}
}

func TestSnapshotObservationUsageRejectsChangedAttributionAndUnsafeOutput(t *testing.T) {
	for _, fault := range []string{"version", "parent_version", "parent_state", "run", "attempt", "group", "inputs", "digest", "request", "count", "missing", "extra", "name", "surface", "round", "next_round", "next_phase", "cursor", "status", "denied_result", "error_result", "missing_reason", "ok_reason", "json", "empty", "result_bound", "reason_bound", "invalid_utf8", "truncated_as_ok", "false_truncation", "short_truncation", "missing_preview", "extra_truncation_field", "duplicate_truncation_field", "policy", "skill", "foreign_run", "input_field", "unknown_tool", "parent_digest"} {
		t.Run(fault, func(t *testing.T) {
			parent, artifact := snapshotUsageFixture(t)
			o := &artifact.Observations[0]
			switch fault {
			case "version":
				artifact.Version++
			case "parent_version":
				parent.Version++
			case "parent_state":
				parent.State = ModelReductionValidated
			case "run":
				artifact.RunID = "other"
			case "attempt":
				artifact.AttemptID = "other"
			case "group":
				artifact.ParentGroupDigest = snapshotTestDigest([]byte("other"))
			case "inputs":
				artifact.ParentInputDigest = snapshotTestDigest([]byte("other"))
			case "digest":
				artifact.RequestDigest = "unbound"
			case "request":
				artifact.Requests = []ToolRequest{{Name: "get_merge_request", Round: 1}}
			case "count":
				parent.ToolRequests = nil
				artifact.Requests = nil
			case "missing":
				artifact.Observations = nil
			case "extra":
				artifact.Observations = append(artifact.Observations, *o)
			case "name":
				o.Name = "get_merge_request"
			case "surface":
				o.Surface = "shell"
			case "round":
				o.Round++
			case "next_round":
				parent.NextCursor.Round++
			case "next_phase":
				parent.NextCursor.Phase = ModelRepairFindings
			case "cursor":
				parent.Cursor = ModelCursor{Phase: ModelToolRound, Round: MaxModelToolRounds}
			case "status":
				o.Status = "success"
			case "denied_result":
				o.Status = "denied"
				o.Reason = "denied"
			case "error_result":
				o.Status = "error"
				o.Reason = "error"
			case "missing_reason":
				o.Status = "denied"
				o.Result = ""
			case "ok_reason":
				o.Reason = "incomplete"
			case "json":
				o.Result = "not-json"
			case "empty":
				o.Result = ""
			case "result_bound":
				o.Result = `"` + strings.Repeat("x", MaxSnapshotToolResultBytes) + `"`
			case "reason_bound":
				o.Status = "error"
				o.Result = ""
				o.Reason = strings.Repeat("x", MaxModelToolReasonBytes+1)
			case "invalid_utf8":
				o.Result = string([]byte{'"', 0xff, '"'})
			case "truncated_as_ok":
				o.Result = `{"truncated":true,"original_bytes":5000,"preview":"partial"}`
			case "false_truncation", "short_truncation", "missing_preview", "extra_truncation_field", "duplicate_truncation_field":
				o.Status = "truncated"
				o.Reason = "incomplete"
				switch fault {
				case "false_truncation":
					o.Result = `{"truncated":false,"original_bytes":5000,"preview":"partial"}`
				case "short_truncation":
					o.Result = `{"truncated":true,"original_bytes":10,"preview":"partial"}`
				case "missing_preview":
					o.Result = `{"truncated":true,"original_bytes":5000}`
				case "extra_truncation_field":
					o.Result = `{"truncated":true,"original_bytes":5000,"preview":"partial","complete":true}`
				case "duplicate_truncation_field":
					o.Result = `{"truncated":false,"truncated":true,"original_bytes":5000,"preview":"partial"}`
				}
			case "policy":
				parent.Source.Policy.AllowedCapabilities = []string{"model.review"}
			case "skill":
				parent.Source.SkillActivations = nil
			case "foreign_run":
				parent.ToolRequests[0].Input = map[string]any{"run": "foreign"}
			case "input_field":
				parent.ToolRequests[0].Input = map[string]any{"path": "foreign"}
			case "unknown_tool":
				parent.ToolRequests[0].Name = "publish_final"
				o.Name = "publish_final"
				o.Surface = ""
			case "parent_digest":
				parent.GroupDigest = "unbound"
				artifact.ParentGroupDigest = "unbound"
			}
			if fault == "foreign_run" || fault == "input_field" || fault == "unknown_tool" {
				// Preserve internally consistent request hashes: these must fail on
				// scope enforcement, not simply because a digest was left stale.
				requests, _ := json.Marshal(artifact.Requests)
				artifact.RequestDigest = snapshotTestDigest(requests)
			}
			usage, err := artifact.SnapshotUsage(parent)
			if err == nil || usage != (ResourceLimitScope{}) {
				t.Fatalf("%s exposed accepted usage for unqualified observations: %#v %v", fault, usage, err)
			}
		})
	}
}

func TestSnapshotObservationUsageScopeDenialsRemainChargeable(t *testing.T) {
	for _, fault := range []string{"policy", "skill", "foreign_run", "input_field", "unknown_tool"} {
		t.Run(fault, func(t *testing.T) {
			parent, artifact := snapshotUsageFixture(t)
			switch fault {
			case "policy":
				parent.Source.Policy.AllowedCapabilities = nil
			case "skill":
				parent.Source.SkillActivations = nil
			case "foreign_run":
				parent.ToolRequests[0].Input = map[string]any{"run": "foreign"}
			case "input_field":
				parent.ToolRequests[0].Input = map[string]any{"path": "foreign"}
			case "unknown_tool":
				parent.ToolRequests[0].Name = "publish_final"
			}
			requests, _ := json.Marshal(artifact.Requests)
			artifact.RequestDigest = snapshotTestDigest(requests)
			artifact.Observations[0] = ToolObservation{Name: parent.ToolRequests[0].Name, Round: 1, Surface: SnapshotToolSurface(parent.ToolRequests[0].Name), Status: "denied", Reason: "not allowed"}
			usage, err := artifact.SnapshotUsage(parent)
			if err != nil || usage.ToolCalls != 1 || usage.ObservationBytes <= 0 {
				t.Fatalf("denial disappeared from accounting: %#v %v", usage, err)
			}
		})
	}
}

func TestSnapshotObservationUsageChargesExactEncodedBytesAndReturnsNoPartialUsage(t *testing.T) {
	parent, artifact := snapshotUsageFixture(t)
	artifact.Observations[0].Result = `{"text":"\u754c\\\"<&"}`
	encoded, err := json.Marshal(artifact.Observations[0])
	if err != nil {
		t.Fatal(err)
	}
	usage, err := artifact.SnapshotUsage(parent)
	if err != nil || usage.ObservationBytes != int64(len(encoded)) {
		t.Fatalf("escaped encoded bytes were undercounted: %#v %v", usage, err)
	}
	parent.ToolRequests = append(parent.ToolRequests, parent.ToolRequests[0])
	artifact.Requests = parent.ToolRequests
	requests, _ := json.Marshal(artifact.Requests)
	artifact.RequestDigest = snapshotTestDigest(requests)
	bad := artifact.Observations[0]
	bad.Status = "unqualified"
	artifact.Observations = append(artifact.Observations, bad)
	usage, err = artifact.SnapshotUsage(parent)
	if err == nil || usage != (ResourceLimitScope{}) {
		t.Fatalf("later invalid observation exposed partial accounting: %#v %v", usage, err)
	}
}

func TestSnapshotObservationUsageRoundAndProposalBounds(t *testing.T) {
	for count := 1; count <= MaxModelToolRequests+1; count++ {
		for round := 1; round <= MaxModelToolRounds+1; round++ {
			parent, artifact := snapshotUsageFixture(t)
			if round > 1 {
				parent.Cursor = ModelCursor{Phase: ModelToolRound, Round: round - 1}
			}
			parent.NextCursor.Round = round
			parent.ToolRequests[0].Round = round
			artifact.Observations[0].Round = round
			for len(parent.ToolRequests) < count {
				parent.ToolRequests = append(parent.ToolRequests, parent.ToolRequests[0])
				artifact.Observations = append(artifact.Observations, artifact.Observations[0])
			}
			artifact.Requests = parent.ToolRequests
			requests, _ := json.Marshal(artifact.Requests)
			artifact.RequestDigest = snapshotTestDigest(requests)
			usage, err := artifact.SnapshotUsage(parent)
			if count > MaxModelToolRequests || round > MaxModelToolRounds {
				if err == nil || usage != (ResourceLimitScope{}) {
					t.Fatalf("count=%d round=%d exceeded bounds: %#v %v", count, round, usage, err)
				}
			} else if err != nil || usage.ToolCalls != int64(count) {
				t.Fatalf("qualified count=%d round=%d rejected: %#v %v", count, round, usage, err)
			}
		}
	}
}

func TestSnapshotObservationUsagePreservesOrderedDuplicateRequests(t *testing.T) {
	parent, artifact := snapshotUsageFixture(t)
	parent.ToolRequests = append(parent.ToolRequests, parent.ToolRequests[0])
	artifact.Requests = parent.ToolRequests
	requests, _ := json.Marshal(artifact.Requests)
	artifact.RequestDigest = snapshotTestDigest(requests)
	artifact.Observations = append(artifact.Observations, artifact.Observations[0])
	usage, err := artifact.SnapshotUsage(parent)
	if err != nil || usage.ToolCalls != 2 {
		t.Fatalf("duplicate logical proposals were deduplicated or rejected: %#v %v", usage, err)
	}
}
