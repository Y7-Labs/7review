package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/review"
)

func (a *Authority) validateModelCheckpointTx(ctx context.Context, tx *sql.Tx, i orchestrator.DispatchIntent) error {
	if err := orchestrator.ValidateModelCheckpoint(i); err != nil {
		return err
	}
	if len(i.ModelCheckpoint) == 0 {
		return nil
	}
	cp, err := review.DecodeModelCheckpoint(i.ModelCheckpoint)
	if err != nil {
		return err
	}
	if i.Job == nil {
		return nil
	}
	aggregate, err := a.readAggregateTx(ctx, tx, i.Job.RunID)
	if err != nil {
		return err
	}
	// Enrichment may add selected evidence, memory and observations, but cannot
	// replace the accepted SCM snapshot or the trusted policy projection.
	for _, pair := range [][2]any{{cp.Source.SCM, aggregate.Source.SCM}, {cp.Source.Policy, aggregate.Source.Policy}} {
		left, err := json.Marshal(pair[0])
		if err != nil {
			return err
		}
		right, err := json.Marshal(pair[1])
		if err != nil {
			return err
		}
		if !bytes.Equal(left, right) {
			return errors.New("model checkpoint changed accepted SCM or policy")
		}
	}
	return nil
}
