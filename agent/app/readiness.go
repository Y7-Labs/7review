package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Y4NN777/7review/agent/accounting"
)

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	status := s.readiness(ctx)

	w.Header().Set("Content-Type", "application/json")
	if !status.Ready {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(status)
}

type readinessStatus struct {
	Inbox        *accounting.InboxHealth `json:"inbox,omitempty"`
	Ready        bool                    `json:"ready"`
	Dependencies map[string]string       `json:"dependencies"`
	Queue        queueStatus             `json:"queue,omitempty"`
}

func (s *readinessStatus) markDown(name, message string) {
	s.Ready = false
	s.Dependencies[name] = message
}

func (s *Server) readiness(ctx context.Context) readinessStatus {
	status := readinessStatus{
		Ready:        true,
		Dependencies: make(map[string]string),
	}
	if s == nil || s.pipeline == nil {
		status.markDown("pipeline", "pipeline is not configured")
	} else {
		status.Dependencies["pipeline"] = "ok"
	}
	if s == nil || s.pipeline == nil || s.pipeline.Orchestrator == nil {
		status.markDown("orchestrator", "orchestrator is not configured")
	} else {
		status.Dependencies["orchestrator"] = "ok"
	}
	if s == nil || s.work == nil {
		status.markDown("queue", "worker queue is not configured")
	} else {
		status.Queue = s.queueStatus()
		status.Dependencies["queue"] = fmt.Sprintf("ok depth=%d capacity=%d", status.Queue.Depth, status.Queue.Capacity)
	}
	if s == nil || s.pipeline == nil || s.pipeline.Jobs == nil {
		status.markDown("run_store", "run store is not configured")
	} else if _, err := s.pipeline.Jobs.List(ctx); err != nil {
		status.markDown("run_store", err.Error())
	} else {
		status.Dependencies["run_store"] = "ok"
	}
	if s != nil && s.pipeline != nil && s.pipeline.Canonical != nil {
		if s.accountingRuntime == nil || s.accountingRuntime.Authority == nil {
			status.markDown("durable_inbox", "durable inbox is not configured")
		} else if inbox, err := s.accountingRuntime.Authority.CheckInbox(ctx); err != nil {
			status.markDown("durable_inbox", "durable inbox unavailable")
		} else {
			status.Inbox = inbox
			status.Dependencies["durable_inbox"] = "ok (storage only; not model-route qualification)"
		}
	}
	if s == nil || s.pipeline == nil || s.pipeline.ContextReducer == nil {
		status.markDown("headroom", "headroom reducer is not configured")
	} else if err := s.pipeline.ContextReducer.Check(ctx); err != nil {
		status.Ready = false
		status.Dependencies["headroom"] = err.Error()
	} else {
		status.Dependencies["headroom"] = "ok"
	}
	if s == nil || s.pipeline == nil || s.pipeline.Memory == nil {
		status.markDown("mempalace", "mempalace store is not configured")
	} else if err := s.pipeline.Memory.Check(ctx); err != nil {
		status.markDown("mempalace", err.Error())
	} else {
		status.Dependencies["mempalace"] = "ok"
	}
	return status
}

type queueStatus struct {
	Depth     int    `json:"depth"`
	Capacity  int    `json:"capacity"`
	Available int    `json:"available"`
	Enqueued  uint64 `json:"enqueued"`
	Completed uint64 `json:"completed"`
	Failed    uint64 `json:"failed"`
}

func (s *Server) queueStatus() queueStatus {
	if s == nil || s.work == nil {
		return queueStatus{}
	}
	depth := len(s.work)
	capacity := cap(s.work)
	return queueStatus{
		Depth:     depth,
		Capacity:  capacity,
		Available: capacity - depth,
		Enqueued:  s.stats.enqueued.Load(),
		Completed: s.stats.completed.Load(),
		Failed:    s.stats.failed.Load(),
	}
}
