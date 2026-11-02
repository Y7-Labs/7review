package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MaxModelToolRequests            = 5
	MaxModelToolNameBytes           = 128
	MaxModelToolReasonBytes         = 1024
	MaxModelToolInputBytes          = 4096
	MaxSnapshotToolResultBytes      = 4096
	MaxModelToolObservationBytes    = 12 << 10
	MaxModelToolObservationSetBytes = 128 << 10
)

// ModelToolObservations is a local snapshot rendering, not a dispatch grant,
// financial receipt, validated assessment, approval or publication.
type ModelToolObservations struct {
	Version                                             int
	RunID, AttemptID                                    string
	ParentGroupDigest, ParentInputDigest, RequestDigest string
	Requests                                            []ToolRequest
	Observations                                        []ToolObservation
}

// SnapshotToolSurface maps the closed snapshot-only tool vocabulary. Unknown
// names have no surface; callers must not substitute a general tool executor.
func SnapshotToolSurface(name string) string {
	switch name {
	case "get_merge_request", "get_changed_files", "list_discussions", "get_inline_positions":
		return "scm-api"
	case "get_diff_summary":
		return "diff-analyzer"
	case "get_selected_context":
		return "corpus-selector"
	default:
		return ""
	}
}

// SnapshotUsage checks shape and attribution against an already verified parent.
// It is not provenance verification, a durable charge, capacity admission or a
// continuation grant. Every proposal evaluation (including denial/error) counts;
// bytes are the sum of encoded observations, not rune counts or result payloads.
// No provider billing or active-time measurement is inferred from local data.
func (o ModelToolObservations) SnapshotUsage(parent ModelReduction) (ResourceLimitScope, error) {
	var zero ResourceLimitScope
	if o.Version != 1 || parent.Version != 1 || parent.State != ModelReductionNeedsTools || parent.NextCursor == nil || parent.Cursor.validate() != nil || parent.NextCursor.validate() != nil || parent.Cursor.BatchIndex != 0 {
		return zero, errors.New("snapshot observations require a qualified pending tool cursor")
	}
	wantRound := 1
	switch parent.Cursor.Phase {
	case ModelReviewBatches:
	case ModelToolRound:
		wantRound = parent.Cursor.Round + 1
	default:
		return zero, errors.New("unsupported snapshot observation parent phase")
	}
	if wantRound > MaxModelToolRounds || parent.NextCursor.Phase != ModelToolRound || parent.NextCursor.Round != wantRound {
		return zero, errors.New("snapshot observation round changed")
	}
	if o.RunID == "" || o.AttemptID == "" || o.RunID != parent.Source.Run.ID || o.AttemptID != parent.Source.Attempt.AttemptID || !snapshotDigestValid(parent.GroupDigest) || !snapshotDigestValid(parent.InputDigest) || o.ParentGroupDigest != parent.GroupDigest || o.ParentInputDigest != parent.InputDigest {
		return zero, errors.New("snapshot observation parent attribution changed")
	}
	if len(parent.ToolRequests) == 0 || len(parent.ToolRequests) > MaxModelToolRequests || len(o.Requests) != len(parent.ToolRequests) || len(o.Observations) != len(parent.ToolRequests) {
		return zero, errors.New("snapshot observation membership changed")
	}
	requests, err := json.Marshal(o.Requests)
	if err != nil {
		return zero, err
	}
	want, err := json.Marshal(parent.ToolRequests)
	digest := sha256.Sum256(requests)
	if err != nil || !bytes.Equal(requests, want) || o.RequestDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return zero, errors.New("snapshot observation request digest changed")
	}
	usage := ResourceLimitScope{ToolCalls: int64(len(o.Requests))}
	for index, request := range o.Requests {
		input, err := json.Marshal(request.Input)
		if err != nil || len(input) > MaxModelToolInputBytes || request.Round != wantRound || request.Name == "" || request.Name != strings.TrimSpace(request.Name) || len(request.Name) > MaxModelToolNameBytes || len(request.Reason) > MaxModelToolReasonBytes || !utf8.ValidString(request.Name) || !utf8.ValidString(request.Reason) {
			return zero, errors.New("snapshot observation request is outside the supported bound")
		}
		observation := o.Observations[index]
		if observation.Name != request.Name || observation.Round != request.Round || observation.Surface != SnapshotToolSurface(request.Name) || !utf8.ValidString(observation.Reason) || !utf8.ValidString(observation.Result) || len(observation.Reason) > MaxModelToolReasonBytes || len(observation.Result) > MaxSnapshotToolResultBytes {
			return zero, errors.New("snapshot observation identity or output bound changed")
		}
		if observation.Status != "denied" && !snapshotRequestAllowed(parent.Source, request) {
			return zero, errors.New("snapshot observation bypasses the read-only scope")
		}
		switch observation.Status {
		case "denied", "error":
			if observation.Result != "" || strings.TrimSpace(observation.Reason) == "" {
				return zero, errors.New("unavailable snapshot evidence cannot contain a result")
			}
		case "ok":
			var object map[string]json.RawMessage
			_ = json.Unmarshal([]byte(observation.Result), &object)
			if _, reserved := object["truncated"]; reserved || observation.Reason != "" || !json.Valid([]byte(observation.Result)) {
				return zero, errors.New("snapshot success contains invalid or incomplete evidence")
			}
		case "truncated":
			if strings.TrimSpace(observation.Reason) == "" || !validSnapshotTruncation(observation.Result) {
				return zero, errors.New("snapshot truncation metadata is unavailable or inconsistent")
			}
		default:
			return zero, errors.New("unsupported snapshot observation status")
		}
		raw, err := json.Marshal(observation)
		if err != nil || len(raw) > MaxModelToolObservationBytes {
			return zero, errors.New("snapshot observation exceeds the encoded output bound")
		}
		usage.ObservationBytes += int64(len(raw))
	}
	raw, err := json.Marshal(o)
	if err != nil || len(raw) > MaxModelToolObservationSetBytes {
		return zero, errors.New("snapshot observation set exceeds the storage bound")
	}
	return usage, nil
}

func snapshotRequestAllowed(source Source, request ToolRequest) bool {
	surface := SnapshotToolSurface(request.Name)
	if surface == "" || !source.Policy.AllowsCapability("repo.read") {
		return false
	}
	for key, value := range request.Input {
		run, ok := value.(string)
		if key != "run" || !ok || run == "" || run != source.Run.ID {
			return false
		}
	}
	for _, activation := range source.SkillActivations {
		for _, allowed := range activation.AllowedTools {
			if allowed == surface {
				return true
			}
		}
	}
	return false
}

func snapshotDigestValid(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(raw) == sha256.Size && value == "sha256:"+hex.EncodeToString(raw)
}

func validSnapshotTruncation(value string) bool {
	var envelope struct {
		Truncated     bool   `json:"truncated"`
		OriginalBytes int    `json:"original_bytes"`
		Preview       string `json:"preview"`
	}
	if json.Unmarshal([]byte(value), &envelope) != nil || !envelope.Truncated || envelope.OriginalBytes <= MaxSnapshotToolResultBytes || envelope.Preview == "" || len(envelope.Preview) >= envelope.OriginalBytes {
		return false
	}
	// The renderer emits this canonical envelope. Re-encoding also rejects extra
	// fields, duplicate keys and lossy Unicode decoding instead of hiding them.
	raw, err := json.Marshal(envelope)
	return err == nil && bytes.Equal(raw, []byte(value))
}
