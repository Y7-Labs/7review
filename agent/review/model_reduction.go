package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type ModelReductionState string

const (
	ModelReductionValidated           ModelReductionState = "validated"
	ModelReductionNeedsTools          ModelReductionState = "needs_tools"
	ModelReductionNeedsFindingsRepair ModelReductionState = "needs_findings_repair"
	ModelReductionNeedsCoverageRepair ModelReductionState = "needs_coverage_repair"
	ModelReductionBlocked             ModelReductionState = "blocked"
)

type ModelResultRef struct {
	ActionID, RequestDigest, ResponseDigest, CheckpointDigest string
}

type ModelFindingRejection struct {
	Finding Finding
	Reason  string
}

// ModelReduction is local evidence plus a proposed next cursor. It grants no
// model/tool, publication, settlement or job-ownership authority.
type ModelReduction struct {
	Version                  int
	GroupDigest, InputDigest string
	Inputs                   []ModelResultRef
	Cursor                   ModelCursor
	Validation               ModelValidation
	State                    ModelReductionState
	Source                   Source
	RawResponses             []string
	NextCursor               *ModelCursor
	ToolRequests             []ToolRequest
	RepairInput, Reason      string
	Rejected                 []ModelFindingRejection
	Routing                  *ModelRouting `json:",omitempty"`
}

func ModelCheckpointGroupDigest(raw []byte) (string, error) {
	cp, err := DecodeModelCheckpoint(raw)
	if err != nil {
		return "", err
	}
	cp.Cursor.BatchIndex = 0
	canonical, err := json.Marshal(cp)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
