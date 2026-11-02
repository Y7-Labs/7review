package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/Y4NN777/7review/agent/review"
)

type ModelToolObserverV1 struct{}

// ObserveModelTools renders only the supplied snapshot. It neither reads SCM or
// runtime configuration nor persists, charges, dispatches or advances the job.
func (ModelToolObserverV1) ObserveModelTools(ctx context.Context, parent review.ModelReduction) (review.ModelToolObservations, error) {
	var empty review.ModelToolObservations
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if parent.Version != 1 || parent.State != review.ModelReductionNeedsTools || parent.NextCursor == nil || len(parent.ToolRequests) == 0 || len(parent.ToolRequests) > review.MaxModelToolRequests || parent.Source.Report.Draft != "" || parent.Source.Report.Final != "" || parent.Source.Run.ID == "" || parent.Source.Attestation == nil || parent.Source.Diff == nil || len(parent.Source.Diff.Files) == 0 {
		return empty, errors.New("qualified pending tool parent unavailable")
	}
	cursor, err := json.Marshal(review.ModelCheckpoint{Version: 1, Cursor: parent.Cursor})
	if err != nil {
		return empty, err
	}
	if _, err = review.DecodeModelCheckpoint(cursor); err != nil {
		return empty, err
	}
	if parent.Validation.Version != 1 || math.IsNaN(parent.Validation.MinConfidence) || math.IsInf(parent.Validation.MinConfidence, 0) || parent.Validation.MinConfidence < 0 || parent.Validation.MinConfidence > 1 {
		return empty, errors.New("tool parent validation contract unavailable")
	}
	wantRound := 1
	if parent.Cursor.Phase == review.ModelToolRound {
		wantRound = parent.Cursor.Round + 1
	} else if parent.Cursor.Phase != review.ModelReviewBatches {
		return empty, errors.New("tool request parent phase is unsupported")
	}
	if wantRound > review.MaxModelToolRounds || parent.NextCursor.Phase != review.ModelToolRound || parent.NextCursor.Round != wantRound || parent.NextCursor.BatchCount != 0 || parent.NextCursor.BatchIndex != 0 {
		return empty, errors.New("tool observation round is outside the qualified cursor")
	}
	if !validModelToolDigest(parent.GroupDigest) || len(parent.Inputs) == 0 || len(parent.Inputs) > 128 {
		return empty, errors.New("tool parent provenance unavailable")
	}
	refs, err := json.Marshal(parent.Inputs)
	if err != nil || modelDigest(refs) != parent.InputDigest {
		return empty, errors.New("tool parent input digest changed")
	}
	for _, input := range parent.Inputs {
		if input.ActionID == "" || !validModelToolDigest(input.RequestDigest) || !validModelToolDigest(input.ResponseDigest) || !validModelToolDigest(input.CheckpointDigest) {
			return empty, errors.New("invalid tool parent input reference")
		}
	}
	wantInputs := 1
	if parent.Cursor.Phase == review.ModelReviewBatches {
		wantInputs = parent.Cursor.BatchCount
	}
	if len(parent.Inputs) != wantInputs || parent.Cursor.BatchIndex != 0 || len(parent.RawResponses) < wantInputs {
		return empty, errors.New("tool parent is missing complete response slots")
	}
	responses := parent.RawResponses[len(parent.RawResponses)-wantInputs:]
	for index, response := range responses {
		if len(response) > 8<<20 || modelDigest([]byte(response)) != parent.Inputs[index].ResponseDigest {
			return empty, errors.New("tool parent response digest changed")
		}
	}
	_, _, proposals, status := parseReviewResponses(responses)
	if isFatalModelParseStatus(status) || len(proposals) == 0 {
		return empty, errors.New("recorded response is not a complete tool proposal plan")
	}
	proposals = markToolRequestRound(proposals, wantRound)
	proposed, err := json.Marshal(proposals)
	if err != nil {
		return empty, err
	}
	declared, err := json.Marshal(parent.ToolRequests)
	if err != nil || !bytes.Equal(proposed, declared) {
		return empty, errors.New("tool proposals differ from recorded response")
	}
	if _, err = review.NewVerifiedContext(parent.Source.Request, parent.Source.Attempt, parent.Source.Execution, *parent.Source.Attestation); err != nil {
		return empty, err
	}
	for _, request := range parent.ToolRequests {
		if request.Round != wantRound || request.Name != strings.TrimSpace(request.Name) {
			return empty, errors.New("tool request changed normalized identity or round")
		}
		if err = validateSnapshotToolRequestShape(request); err != nil {
			return empty, err
		}
	}
	raw, err := json.Marshal(parent)
	if err != nil || len(raw) > 32<<20 {
		return empty, errors.New("tool parent exceeds snapshot observation bound")
	}
	var frozen review.ModelReduction
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&frozen); err != nil {
		return empty, err
	}
	requests, err := json.Marshal(frozen.ToolRequests)
	if err != nil {
		return empty, err
	}
	result := review.ModelToolObservations{Version: 1, RunID: frozen.Source.Run.ID, AttemptID: frozen.Source.Attempt.AttemptID, ParentGroupDigest: frozen.GroupDigest, ParentInputDigest: frozen.InputDigest, RequestDigest: modelDigest(requests), Requests: frozen.ToolRequests}
	rc := &review.Context{Source: frozen.Source}
	for _, request := range frozen.ToolRequests {
		if err = ctx.Err(); err != nil {
			return empty, err
		}
		observation := observeSnapshotTool(rc, request)
		encoded, err := json.Marshal(observation)
		if err != nil || len(encoded) > review.MaxModelToolObservationBytes {
			return empty, errors.New("tool observation exceeds qualified output bound")
		}
		result.Observations = append(result.Observations, observation)
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > review.MaxModelToolObservationSetBytes {
		return empty, errors.New("tool observation set exceeds qualified storage bound")
	}
	if _, err = result.SnapshotUsage(frozen); err != nil {
		return empty, err
	}
	return result, nil
}

func validModelToolDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(raw) == sha256.Size && value == "sha256:"+hex.EncodeToString(raw)
}
