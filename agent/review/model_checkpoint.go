package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
)

const MaxModelCheckpointBytes = 16 << 20
const MaxModelToolRounds = 3

type ModelPhase string

const (
	ModelReviewBatches  ModelPhase = "review_batches"
	ModelToolRound      ModelPhase = "tool_round"
	ModelRepairFindings ModelPhase = "repair_findings"
	ModelRepairCoverage ModelPhase = "repair_coverage"
)

type ModelCursor struct {
	Phase                         ModelPhase
	BatchIndex, BatchCount, Round int
	PriorCoverage                 []SkillCoverage
}

// ModelCheckpoint is pre-action state, not validated output or scheduling authority.
// Its bytes are bound into the immutable action before any provider I/O.
type ModelCheckpoint struct {
	Version      int
	Cursor       ModelCursor
	Source       Source
	RawResponses []string
	Validation   *ModelValidation
	Routing      *ModelRouting `json:",omitempty"`
}

type ModelValidation struct {
	Version       int
	MinConfidence float64
}

type modelValidationKey struct{}

// A nil contract deliberately leaves custom validators unqualified for replay.
func WithModelValidation(ctx context.Context, contract *ModelValidation) context.Context {
	if contract == nil {
		return context.WithValue(ctx, modelValidationKey{}, (*ModelValidation)(nil))
	}
	copy := *contract
	return context.WithValue(ctx, modelValidationKey{}, &copy)
}

type modelCheckpointKey struct{}

func (c ModelCursor) validate() error {
	if c.BatchIndex < 0 || c.BatchCount < 0 || c.Round < 0 {
		return errors.New("negative model cursor")
	}
	switch c.Phase {
	case ModelReviewBatches:
		if c.BatchCount < 1 || c.BatchIndex >= c.BatchCount || c.Round != 0 || len(c.PriorCoverage) != 0 {
			return errors.New("invalid review batch cursor")
		}
	case ModelToolRound:
		if c.Round < 1 || c.Round > MaxModelToolRounds || c.BatchCount != 0 || c.BatchIndex != 0 {
			return errors.New("invalid tool round cursor")
		}
	case ModelRepairFindings, ModelRepairCoverage:
		if c.Round != 0 || c.BatchCount != 0 || c.BatchIndex != 0 {
			return errors.New("invalid repair cursor")
		}
	default:
		return errors.New("unsupported model continuation phase")
	}
	return nil
}

func DecodeModelCheckpoint(raw []byte) (ModelCheckpoint, error) {
	var cp ModelCheckpoint
	if len(raw) == 0 || len(raw) > MaxModelCheckpointBytes {
		return cp, errors.New("model checkpoint exceeds storage bound or is absent")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&cp); err != nil {
		return cp, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return cp, errors.New("trailing model checkpoint data")
	}
	if cp.Version != 1 {
		return cp, errors.New("unsupported model checkpoint version")
	}
	if cp.Validation != nil && (cp.Validation.Version != 1 || math.IsNaN(cp.Validation.MinConfidence) || cp.Validation.MinConfidence < 0 || cp.Validation.MinConfidence > 1) {
		return cp, errors.New("unsupported model validation contract")
	}
	if err := cp.Routing.Validate(); err != nil {
		return cp, err
	}
	return cp, cp.Cursor.validate()
}

func WithModelCheckpoint(ctx context.Context, rc *Context, cursor ModelCursor) (context.Context, error) {
	if rc == nil {
		return nil, errors.New("model checkpoint requires review context")
	}
	if err := cursor.validate(); err != nil {
		return nil, err
	}
	frozen := rc.Clone()
	validation, _ := ctx.Value(modelValidationKey{}).(*ModelValidation)
	routing, _ := ctx.Value(modelRoutingKey{}).(*ModelRouting)
	raw, err := json.Marshal(ModelCheckpoint{Version: 1, Cursor: cursor, Source: frozen.Source, RawResponses: frozen.AllFindings(), Validation: validation, Routing: routing})
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxModelCheckpointBytes {
		return nil, errors.New("model checkpoint exceeds storage bound")
	}
	if _, err = DecodeModelCheckpoint(raw); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, modelCheckpointKey{}, raw), nil
}

func ModelCheckpointFromContext(ctx context.Context) []byte {
	raw, _ := ctx.Value(modelCheckpointKey{}).([]byte)
	return append([]byte(nil), raw...)
}

// WithModelBatch selects a slot in the already frozen plan, never a new plan.
func WithModelBatch(ctx context.Context, index, count int) (context.Context, error) {
	raw := ModelCheckpointFromContext(ctx)
	if len(raw) == 0 {
		return ctx, nil
	}
	cp, err := DecodeModelCheckpoint(raw)
	if err != nil {
		return nil, err
	}
	if cp.Cursor.Phase != ModelReviewBatches || cp.Cursor.BatchCount != count || index < 0 || index >= count {
		return nil, errors.New("model batch does not match frozen continuation plan")
	}
	cp.Cursor.BatchIndex = index
	raw, err = json.Marshal(cp)
	if err != nil {
		return nil, err
	}
	return context.WithValue(ctx, modelCheckpointKey{}, raw), nil
}
