package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"reflect"
	"time"

	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

var errInboxPanic = errors.New("durable inbox worker panicked; execution not retryable")

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.accountingRuntime == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	item, err := s.accountingRuntime.Authority.LookupInbox(ctx, r.URL.Query().Get("run_id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "durable inbox unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		RunID                string    `json:"run_id"`
		State                string    `json:"state"`
		Deadline             time.Time `json:"deadline"`
		AttemptID            string    `json:"attempt_id,omitempty"`
		JobPhase             string    `json:"job_phase"`
		RecordedUnvalidated  int64     `json:"recorded_unvalidated"`
		UnresolvedActions    int64     `json:"unresolved_actions"`
		PreparedActions      int64     `json:"prepared_actions"`
		ValidatedReductions  int64     `json:"validated_reductions"`
		PendingContinuations int64     `json:"pending_continuations"`
		BlockedReductions    int64     `json:"blocked_reductions"`
	}{RunID: item.RunID, State: item.State, Deadline: item.Deadline, AttemptID: item.AttemptID, JobPhase: item.JobPhase, RecordedUnvalidated: item.RecordedUnvalidated, UnresolvedActions: item.UnresolvedActions, PreparedActions: item.PreparedActions, ValidatedReductions: item.ValidatedReductions, PendingContinuations: item.PendingContinuations, BlockedReductions: item.BlockedReductions})
}

func (s *Server) acceptInbox(req review.Request) (requestReviewResult, error) {
	deadline := time.Now().UTC().Add(s.webhookJobTimeout())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	receipt, err := s.accountingRuntime.Authority.ReplayAggregate(ctx, req)
	if err != nil {
		return requestReviewResult{Status: "rejected", Reason: "durable inbox acceptance failed"}, err
	}
	if receipt == nil {
		if req.EventAction != "manual" {
			if decision := s.reviewPolicyDecision(req); !decision.allowed {
				return requestReviewResult{RunID: requestRunID(req), Status: "ignored", Reason: "ignored by review policy: " + decision.reason}, nil
			}
		}
		prepared, prepareErr := s.pipeline.PrepareService(ctx, req, deadline)
		if prepareErr != nil {
			return requestReviewResult{Status: "rejected", Reason: "trusted preparation failed"}, prepareErr
		}
		if prepared.Ignored {
			return requestReviewResult{RunID: requestRunID(req), Status: "ignored", Reason: prepared.Reason}, nil
		}
		receipt, err = s.accountingRuntime.Authority.AcceptAggregate(ctx, req, prepared.Admission, prepared.Source, prepared.Continuation)
		if err != nil {
			return requestReviewResult{Status: "rejected", Reason: "durable aggregate acceptance failed"}, err
		}
	}
	status := receipt.State
	if status == "pending" {
		status = "enqueued"
	}
	return requestReviewResult{Durable: true, RunID: receipt.RunID, AttemptID: receipt.AttemptID, Status: status}, nil
}

func (s *Server) startInboxWorkers() {
	if s.accountingRuntime == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.inboxCancel = cancel
	workers := s.cfg.WebhookWorkers
	if workers <= 0 {
		workers = 1
	}
	for n := 0; n < workers; n++ {
		s.inboxWorkers.Add(1)
		go func() {
			defer s.inboxWorkers.Done()
			timer := time.NewTimer(250 * time.Millisecond)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				if ctx.Err() != nil {
					return
				}
				// Recovery owns only one immutable prepared action. It never calls
				// RunAccepted or grants a replacement pipeline claim.
				if _, err := s.pipeline.Orchestrator.RecoverPreparedActions(ctx, 8); err != nil {
					if ctx.Err() == nil {
						log.Printf("[inbox] prepared action recovery failed: %v", err)
					}
					timer.Reset(time.Second)
					continue
				}
				recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 5*time.Second)
				_, recoveryErr := s.accountingRuntime.Authority.RecoverActionResults(recoveryCtx, 32)
				if recoveryErr == nil {
					_, recoveryErr = s.accountingRuntime.Authority.RecoverModelReductions(recoveryCtx, 128, pipeline.ModelReducerV1{})
				}
				recoveryCancel()
				if recoveryErr != nil {
					if ctx.Err() == nil {
						log.Printf("[inbox] local action recovery failed: %v", recoveryErr)
					}
					timer.Reset(time.Second)
					continue
				}
				item, err := s.accountingRuntime.Authority.ClaimInbox(ctx)
				if err != nil {
					if ctx.Err() == nil {
						log.Printf("[inbox] durable claim failed: %v", err)
					}
					timer.Reset(time.Second)
					continue
				}
				if item == nil {
					timer.Reset(250 * time.Millisecond)
					continue
				}
				runCtx, stop := context.WithDeadline(ctx, item.Deadline)
				runCtx = review.WithJobClaim(runCtx, review.JobClaim{RunID: item.RunID, Token: item.ClaimToken})
				err = s.runInboxItem(runCtx, item.Request, item.RunID)
				stop()
				// Cancellation/crashes do not turn an uncertain effect into retryable work.
				if ctx.Err() != nil {
					return
				}
				if errors.Is(err, errInboxPanic) {
					log.Printf("[inbox] worker panic left execution uncertain: run=%s", item.RunID)
					timer.Reset(time.Millisecond)
					continue
				}
				finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
				finishErr := s.accountingRuntime.Authority.FinishInbox(finishCtx, item.RunID, item.ClaimToken, err == nil)
				finishCancel()
				if finishErr != nil {
					log.Printf("[inbox] completion unconfirmed: run=%s error=%v", item.RunID, finishErr)
				}
				if err != nil {
					s.stats.failed.Add(1)
				} else {
					s.stats.completed.Add(1)
				}
				timer.Reset(time.Millisecond)
			}
		}()
	}
}

func (s *Server) runInboxItem(ctx context.Context, req review.Request, id string) (err error) {
	defer func() {
		if recover() != nil {
			err = errInboxPanic
		}
	}()
	claim := review.JobClaimFromContext(ctx)
	if claim == nil || claim.RunID != id {
		return errors.New("accepted execution requires matching job claim")
	}
	aggregate, loadErr := s.accountingRuntime.Authority.LoadAggregate(ctx, *claim)
	if loadErr != nil {
		return loadErr
	}
	if !reflect.DeepEqual(aggregate.OriginalRequest, req) {
		return errors.New("accepted execution request mismatch")
	}
	return s.pipeline.RunAccepted(ctx, aggregate)
}
