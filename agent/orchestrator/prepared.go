package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/Y4NN777/7review/agent/review"
)

func (o *Orchestrator) reserveDispatch(ctx context.Context, i DispatchIntent) (DispatchPermit, error) {
	staged, ok := o.budgetAuthority.(PreparedBudgetAuthority)
	if !ok {
		return o.budgetAuthority.ReserveAndBegin(ctx, i)
	}
	p, err := staged.PrepareAction(ctx, i)
	if err != nil {
		return DispatchPermit{}, err
	}
	if p.ID != i.ActionID || p.RequestDigest != i.RequestDigest {
		return DispatchPermit{}, errors.New("invalid prepared receipt")
	}
	if p.AlreadyStarted {
		return DispatchPermit{ID: p.ID, RequestDigest: p.RequestDigest, AlreadyStarted: true}, nil
	}
	worker, err := review.NewOpaqueID("dispatch-")
	if err != nil {
		return DispatchPermit{}, err
	}
	c, err := staged.ClaimPrepared(ctx, p, worker, 30*time.Second)
	if err != nil {
		return DispatchPermit{}, err
	}
	if c.ID != p.ID || c.RequestDigest != p.RequestDigest || c.WorkerID != worker || p.Fence >= math.MaxInt64 || c.Fence != p.Fence+1 || c.LeaseUntil.IsZero() || c.Intent == nil || !reflect.DeepEqual(c.Job, i.Job) {
		return DispatchPermit{}, errors.New("invalid prepared ownership receipt")
	}
	requested, err := json.Marshal(i)
	if err != nil {
		return DispatchPermit{}, err
	}
	stored, err := json.Marshal(c.Intent)
	if err != nil {
		return DispatchPermit{}, err
	}
	if !bytes.Equal(requested, stored) {
		return DispatchPermit{}, errors.New("prepared immutable input changed")
	}
	permit, err := staged.BeginPrepared(ctx, c)
	if err != nil {
		return DispatchPermit{}, err
	}
	if permit.ID != p.ID || permit.RequestDigest != p.RequestDigest || permit.Fence != c.Fence || permit.WorkerID != worker || permit.NotAfter.IsZero() || permit.NotAfter.After(c.LeaseUntil) || (!i.Execution.DeadlineAt.IsZero() && permit.NotAfter.After(i.Execution.DeadlineAt)) {
		return DispatchPermit{}, errors.New("invalid fenced sending receipt")
	}
	return permit, nil
}
