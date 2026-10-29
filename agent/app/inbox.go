package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

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
		RunID    string    `json:"run_id"`
		State    string    `json:"state"`
		Deadline time.Time `json:"deadline"`
	}{item.RunID, item.State, item.Deadline})
}

func (s *Server) acceptInbox(req review.Request) (requestReviewResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	receipt, err := s.accountingRuntime.Authority.AcceptInbox(ctx, req, s.webhookJobTimeout())
	if err != nil {
		return requestReviewResult{Status: "rejected", Reason: "durable inbox acceptance failed"}, err
	}
	status := receipt.State
	if status == "pending" {
		status = "enqueued"
	}
	return requestReviewResult{Durable: true, RunID: receipt.RunID, Status: status}, nil
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
	return s.pipeline.RunIdentified(ctx, req, id)
}
