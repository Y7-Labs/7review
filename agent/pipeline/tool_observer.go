package pipeline

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/Y4NN777/7review/agent/review"
)

const maxSnapshotToolResultBytes = review.MaxSnapshotToolResultBytes

func validateSnapshotToolRequestShape(request review.ToolRequest) error {
	if len(request.Name) == 0 || len(request.Name) > review.MaxModelToolNameBytes || len(request.Reason) > review.MaxModelToolReasonBytes {
		return errors.New("tool request identity or reason exceeds the supported bound")
	}
	raw, err := json.Marshal(request.Input)
	if err != nil || len(raw) > review.MaxModelToolInputBytes {
		return errors.New("tool request input exceeds the supported bound")
	}
	return nil
}

func snapshotToolResult(result any) (string, bool, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return "", false, err
	}
	if len(raw) <= maxSnapshotToolResultBytes {
		return string(raw), false, nil
	}
	limit := maxSnapshotToolResultBytes / 2
	for {
		for limit > 0 && !utf8.Valid(raw[:limit]) {
			limit--
		}
		preview, err := json.Marshal(struct {
			Truncated     bool   `json:"truncated"`
			OriginalBytes int    `json:"original_bytes"`
			Preview       string `json:"preview"`
		}{true, len(raw), string(raw[:limit])})
		if err != nil {
			return "", false, err
		}
		if len(preview) <= maxSnapshotToolResultBytes {
			return string(preview), true, nil
		}
		if limit == 0 {
			return "", false, errors.New("tool result bound cannot contain truncation metadata")
		}
		limit /= 2
	}
}
