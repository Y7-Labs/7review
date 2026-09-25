package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Y4NN777/7review/agent/orchestrator"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

type chatStreamRequest struct {
	Message string `json:"message"`
}

type chatStreamEvent struct {
	Delta string `json:"delta,omitempty"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("run")
	var req chatStreamRequest
	body, err := readBoundedBody(r.Body, chatMaxBodyBytes)
	if err != nil {
		http.Error(w, "chat message too large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(body) > 0 && strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid chat request", http.StatusBadRequest)
			return
		}
	} else {
		req.Message = strings.TrimSpace(string(body))
	}
	if req.Message == "" {
		http.Error(w, "missing message", http.StatusBadRequest)
		return
	}
	if s.pipeline.Orchestrator == nil {
		http.Error(w, "orchestrator is not configured", http.StatusServiceUnavailable)
		return
	}
	if id == "" {
		s.handleOperatorChatStream(w, r, req.Message)
		return
	}
	run, err := s.pipeline.Jobs.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	rc := run.Context
	if rc == nil {
		rc = review.NewContext(run.Request)
		rc.Findings = append([]review.Finding(nil), run.Findings...)
		rc.Source.Report.Draft = run.DraftReport
		rc.Source.Report.Final = run.FinalReport
		if rc.Request.WebURL == "" {
			rc.Request.WebURL = run.WebURL
		}
	}
	system := reviewChatSystemPrompt(*run)
	user := fmt.Sprintf("Engineer message:\n%s", req.Message)
	_ = s.pipeline.Jobs.AppendEvent(r.Context(), id, pipeline.RunEvent{
		Type:    "chat_message",
		Status:  run.Status,
		Message: truncateEventMessage(req.Message),
		Meta: map[string]string{
			"role":  "engineer",
			"bytes": fmt.Sprintf("%d", len(req.Message)),
		},
	})
	responseText, err := s.pipeline.Orchestrator.StreamComplete(r.Context(), rc, orchestrator.RoleFormatter, "review_chat", system, user, func(delta string) error {
		data, _ := json.Marshal(chatStreamEvent{Delta: delta})
		if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", data); writeErr != nil {
			return writeErr
		}
		flusher.Flush()
		return nil
	})
	if err != nil {
		_ = s.pipeline.Jobs.AppendEvent(r.Context(), id, pipeline.RunEvent{
			Type:    "chat_failed",
			Status:  run.Status,
			Message: truncateEventMessage(err.Error()),
			Meta:    map[string]string{"role": "agent"},
		})
		data, _ := json.Marshal(chatStreamEvent{Error: err.Error()})
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}
	_ = s.pipeline.Jobs.AppendEvent(r.Context(), id, pipeline.RunEvent{
		Type:    "chat_response",
		Status:  run.Status,
		Message: truncateEventMessage(responseText),
		Meta: map[string]string{
			"role":  "agent",
			"bytes": fmt.Sprintf("%d", len(responseText)),
		},
	})
	_, _ = fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}

func (s *Server) handleOperatorChatStream(w http.ResponseWriter, r *http.Request, message string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	if answer, ok := deterministicOperatorAnswer(s.cfg, message); ok {
		data, _ := json.Marshal(chatStreamEvent{Delta: answer})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		_, _ = fmt.Fprint(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
		return
	}

	rc := review.NewContext(review.Request{Provider: "operator", ProjectID: "local", Repository: "operator-chat", ChangeID: "chat", Title: message})
	system := operatorChatSystemPrompt()
	user := operatorChatUserMessage(s.cfg, s.operatorMemoryBlock(r.Context(), message), message)
	responseText, err := s.pipeline.Orchestrator.StreamComplete(r.Context(), rc, orchestrator.RoleFormatter, "operator_chat", system, user, func(delta string) error {
		data, _ := json.Marshal(chatStreamEvent{Delta: delta})
		if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", data); writeErr != nil {
			return writeErr
		}
		flusher.Flush()
		return nil
	})
	_ = responseText
	if err != nil {
		data, _ := json.Marshal(chatStreamEvent{Error: err.Error()})
		_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}
	_, _ = fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}
