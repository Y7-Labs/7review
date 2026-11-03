package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/channel"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func (s *Server) routes() {
	if s.gitLabWebhookConfigured() {
		s.mux.HandleFunc("/webhook", gitLabWebhookHandler(s.cfg.WebhookSecret, s.handleWebhookReview, s.handleNativeSCMComment))
		s.mux.HandleFunc("/webhook/gitlab", gitLabWebhookHandler(s.cfg.WebhookSecret, s.handleWebhookReview, s.handleNativeSCMComment))
	} else {
		s.mux.HandleFunc("/webhook", inactiveWebhookHandler("gitlab"))
		s.mux.HandleFunc("/webhook/gitlab", inactiveWebhookHandler("gitlab"))
	}
	if s.gitHubWebhookConfigured() {
		s.mux.HandleFunc("/webhook/github", gitHubWebhookHandler(s.cfg.GitHubWebhookSecret, s.handleWebhookReview, s.handleNativeSCMComment))
	} else {
		s.mux.HandleFunc("/webhook/github", inactiveWebhookHandler("github"))
	}

	s.mux.HandleFunc("/approve", s.requireAuth(s.handleApprove))
	s.mux.HandleFunc("/operator/attempt", s.handleOperatorAttempt)
	s.mux.HandleFunc("/operator/actions", s.handleOperatorActions)
	s.mux.HandleFunc("/operator/finding", s.handleOperatorFinding)
	s.mux.HandleFunc("/operator/commands", s.handleOperatorCommand)
	s.mux.HandleFunc("/publish/final", s.requireAuth(s.handlePublishFinal))
	s.mux.HandleFunc("/runs", s.requireAuth(s.handleRuns))
	s.mux.HandleFunc("/inbox", s.requireAuth(s.handleInbox))
	s.mux.HandleFunc("/run", s.requireAuth(s.handleRun))
	s.mux.HandleFunc("/chat/stream", s.requireAuth(s.handleChatStream))
	s.mux.HandleFunc("/tools", s.requireAuth(s.handleTools))
	s.mux.HandleFunc("/tools/execute", s.requireAuth(s.handleToolExecute))
	s.mux.HandleFunc("/channels/", s.handleChannelInbound)
	s.mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s.mux.HandleFunc("/ready", s.requireAuth(s.handleReady))
}

func (s *Server) handleChannelInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s == nil || s.pipeline == nil || s.pipeline.Channels == nil {
		http.Error(w, "channels are not configured", http.StatusNotFound)
		return
	}
	switch {
	case r.URL.Path == "/channels/twilio/whatsapp":
		s.handleTwilioWhatsAppInbound(w, r)
		return
	case r.URL.Path == "/channels/telegram/webhook":
		s.handleTelegramInbound(w, r)
		return
	}
	channelName := channelNameFromPath(r.URL.Path)
	if r.URL.Path != "/channels/"+channelName+"/inbound" || !s.pipeline.Channels.AllowsGenericInbound(channelName) {
		http.Error(w, "channel endpoint not found", http.StatusNotFound)
		return
	}
	if channelName == "" {
		http.Error(w, "missing channel", http.StatusBadRequest)
		return
	}
	body, err := readBoundedBody(r.Body, chatMaxBodyBytes)
	if err != nil {
		http.Error(w, "channel message too large", http.StatusRequestEntityTooLarge)
		return
	}
	var msg channel.InboundMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "invalid channel payload", http.StatusBadRequest)
		return
	}
	msg.Channel = channelName
	if msg.ReceivedAt.IsZero() {
		msg.ReceivedAt = time.Now().UTC()
	}
	token := firstNonEmptyString(r.Header.Get("X-7Review-Channel-Token"), bearerToken(r.Header.Get("Authorization")))
	if _, err := s.pipeline.Channels.VerifyInbound(channelName, token, msg); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	s.routeChannelMessage(w, r, msg)
}

func (s *Server) handleTwilioWhatsAppInbound(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.pipeline.Channels.ConfigForProvider("twilio_whatsapp")
	if !ok {
		http.Error(w, "twilio_whatsapp channel is not configured", http.StatusNotFound)
		return
	}
	body, err := readBoundedBody(r.Body, chatMaxBodyBytes)
	if err != nil {
		http.Error(w, "twilio payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		http.Error(w, "invalid twilio payload", http.StatusBadRequest)
		return
	}
	webhookURL := firstNonEmptyString(channel.Setting(cfg.Settings, "webhook_url"), requestWebhookURL(r))
	if !channel.VerifyTwilioSignature(channel.Setting(cfg.Settings, "auth_token"), webhookURL, form, r.Header.Get("X-Twilio-Signature")) {
		http.Error(w, "invalid twilio signature", http.StatusUnauthorized)
		return
	}
	msg := channel.ParseTwilioWhatsAppInbound(cfg.Name, form)
	if _, err := s.pipeline.Channels.VerifyInbound(cfg.Name, cfg.InboundToken, msg); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	s.routeChannelMessage(w, r, msg)
}

func (s *Server) handleTelegramInbound(w http.ResponseWriter, r *http.Request) {
	cfg, ok := s.pipeline.Channels.ConfigForProvider("telegram")
	if !ok {
		http.Error(w, "telegram channel is not configured", http.StatusNotFound)
		return
	}
	if !channel.VerifyTelegramWebhookSecret(channel.Setting(cfg.Settings, "webhook_secret"), r.Header.Get("X-Telegram-Bot-Api-Secret-Token")) {
		http.Error(w, "invalid telegram webhook secret", http.StatusUnauthorized)
		return
	}
	body, err := readBoundedBody(r.Body, chatMaxBodyBytes)
	if err != nil {
		http.Error(w, "telegram payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	var update channel.TelegramUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		http.Error(w, "invalid telegram payload", http.StatusBadRequest)
		return
	}
	msg := channel.ParseTelegramInbound(cfg.Name, update)
	if _, err := s.pipeline.Channels.VerifyInbound(cfg.Name, "", msg); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	s.routeChannelMessage(w, r, msg)
}

func (s *Server) routeChannelMessage(w http.ResponseWriter, r *http.Request, msg channel.InboundMessage) {
	result, status, err := s.routeChannelMessageContext(r.Context(), msg)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) routeChannelMessageContext(ctx context.Context, msg channel.InboundMessage) (channel.InboundResult, int, error) {
	result := channel.EvaluateInbound(msg)
	if result.Command == "disposition-finding" && result.Reason != "" {
		return result, http.StatusBadRequest, fmt.Errorf("%s", result.Reason)
	}
	if result.Command != "disposition-finding" && result.RunID == "" {
		return result, http.StatusBadRequest, fmt.Errorf("%s", result.Reason)
	}
	deliveryKey := ""
	if msg.ExternalID != "" {
		deliveryKey = fmt.Sprintf("channel:%q:%q", msg.Channel, msg.ExternalID)
		if !s.claimDelivery(deliveryKey) {
			result.Approved, result.Revised, result.Suppressed, result.Dispositioned = false, false, false, false
			result.Reason = "duplicate channel delivery"
			return result, http.StatusAccepted, nil
		}
	}
	accepted := false
	defer func() {
		if !accepted {
			s.releaseDelivery(deliveryKey)
		}
	}()
	if result.Command == "disposition-finding" {
		if s.cfg == nil || s.cfg.Operators == nil {
			return result, http.StatusServiceUnavailable, fmt.Errorf("durable operator authority unavailable")
		}
		envName, err := s.pipeline.Channels.OperatorTokenEnv(msg.Channel, msg)
		if err != nil {
			return result, http.StatusUnauthorized, err
		}
		credential, ok := os.LookupEnv(envName)
		if !ok || credential == "" {
			return result, http.StatusUnauthorized, fmt.Errorf("mapped operator credential unavailable")
		}
		actor, err := s.cfg.Operators.Authenticate(credential)
		if err != nil {
			return result, http.StatusUnauthorized, err
		}
		result, status, err := s.routeFindingDisposition(ctx, result, actor, nil)
		if err == nil {
			accepted = true
		}
		return result, status, err
	}
	if err := s.pipeline.Jobs.AppendEvent(ctx, result.RunID, pipeline.RunEvent{
		Type:    "channel_message_received",
		Status:  "",
		Message: truncateEventMessage(msg.Text),
		Meta: map[string]string{
			"channel":  result.Channel,
			"sender":   result.Sender,
			"external": msg.ExternalID,
			"approved": strconv.FormatBool(result.Approved),
			"reason":   result.Reason,
		},
	}); err != nil {
		return result, http.StatusNotFound, err
	}
	if result.Approved {
		if err := s.enqueue(workItem{
			name: fmt.Sprintf("channel/approve/%s", result.RunID),
			run: func(ctx context.Context) error {
				return s.pipeline.ApproveRun(ctx, result.RunID, result.FinalReport)
			},
		}); err != nil {
			return result, http.StatusServiceUnavailable, err
		}
	}
	if result.Revised {
		if err := s.enqueue(workItem{
			name: fmt.Sprintf("channel/revise/%s", result.RunID),
			run: func(ctx context.Context) error {
				return s.pipeline.ReviseDraft(ctx, result.RunID, result.Request)
			},
		}); err != nil {
			return result, http.StatusServiceUnavailable, err
		}
	}
	if result.Suppressed {
		if err := s.enqueue(workItem{
			name: fmt.Sprintf("channel/suppress/%s/%s", result.RunID, result.FindingID),
			run: func(ctx context.Context) error {
				return s.pipeline.SuppressFinding(ctx, result.RunID, result.FindingID, result.Request)
			},
		}); err != nil {
			return result, http.StatusServiceUnavailable, err
		}
	}
	accepted = true
	return result, http.StatusAccepted, nil
}

func requestWebhookURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	host := firstNonEmptyString(r.Header.Get("X-Forwarded-Host"), r.Host)
	return scheme + "://" + host + r.URL.RequestURI()
}

func channelNameFromPath(path string) string {
	path = strings.TrimPrefix(path, "/channels/")
	name, _, _ := strings.Cut(path, "/")
	return strings.TrimSpace(name)
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return strings.TrimSpace(header[len("bearer "):])
	}
	return ""
}

func (s *Server) gitLabWebhookConfigured() bool {
	return s != nil && s.cfg != nil && s.cfg.GitLabURL != "" && s.cfg.GitLabToken != "" && s.cfg.WebhookSecret != ""
}

func (s *Server) gitHubWebhookConfigured() bool {
	return s != nil && s.cfg != nil && s.cfg.GitHubAPIURL != "" && s.cfg.GitHubToken != "" && s.cfg.GitHubWebhookSecret != ""
}

func inactiveWebhookHandler(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, provider+" webhook is not configured", http.StatusNotFound)
	}
}

type reviewDispatchResult struct {
	AttemptID string `json:"attempt_id,omitempty"`
	Durable   bool   `json:"durable,omitempty"`
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type requestReviewResult struct {
	AttemptID string `json:"attempt_id,omitempty"`
	Durable   bool   `json:"durable,omitempty"`
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

func (s *Server) handleWebhookReview(req review.Request) (reviewDispatchResult, error) {
	if s.pipeline != nil && s.pipeline.Canonical != nil {
		result, err := s.enqueueReview(req, true)
		return reviewDispatchResult{Durable: result.Durable, RunID: result.RunID, AttemptID: result.AttemptID, Status: result.Status, Reason: result.Reason}, err
	}
	decision := s.reviewPolicyDecision(req)
	runID := requestRunID(req)
	if !decision.allowed {
		log.Printf("[server] webhook review ignored by policy: run=%s reason=%s", runID, decision.reason)
		return reviewDispatchResult{RunID: runID, Status: "ignored", Reason: "ignored by review policy: " + decision.reason}, nil
	}
	result, err := s.enqueueReview(req, true)
	return reviewDispatchResult{Durable: result.Durable, RunID: result.RunID, Status: result.Status, Reason: result.Reason}, err
}

func (s *Server) requestReview(ctx context.Context, req review.Request) (requestReviewResult, error) {
	req.DeliveryID = ""
	req.PayloadDigest = ""
	req.EventAction = "manual"
	result, err := s.enqueueReview(req, false)
	return result, err
}

func (s *Server) enqueueReview(req review.Request, webhook bool) (requestReviewResult, error) {
	runID := requestRunID(req)
	canonical := s.pipeline != nil && s.pipeline.Canonical != nil
	if canonical {
		if s.accountingRuntime == nil || s.accountingRuntime.Authority == nil {
			return requestReviewResult{Status: "rejected", Reason: "durable inbox unavailable"}, fmt.Errorf("canonical intake requires a durable inbox")
		}
		return s.acceptInbox(req)
	}
	if runID == "" {
		return requestReviewResult{Status: "rejected", Reason: "review request is missing run identity"}, fmt.Errorf("review request is missing run identity")
	}
	if s.isRunActive(runID) {
		return requestReviewResult{RunID: runID, Status: "rejected", Reason: "review already running"}, fmt.Errorf("review already running for %s", runID)
	}
	if s.pipeline != nil && s.pipeline.Jobs != nil {
		if run, err := s.pipeline.Jobs.Get(context.Background(), runID); err == nil && run.Status == pipeline.StatusRunning {
			return requestReviewResult{RunID: runID, Status: "rejected", Reason: "review already running"}, fmt.Errorf("review already running for %s", runID)
		}
	}
	name := fmt.Sprintf("%s/%s/%s", req.Provider, req.ProjectID, firstNonEmptyString(req.ChangeID, strconv.Itoa(req.MRIID)))
	deliveryKey := req.Provider + ":" + req.DeliveryID
	if webhook && req.DeliveryID != "" && !s.claimDelivery(deliveryKey) {
		log.Printf("[server] duplicate webhook delivery ignored: %s", deliveryKey)
		return requestReviewResult{RunID: runID, Status: "ignored", Reason: "duplicate webhook delivery"}, nil
	}
	s.markRunActive(runID)
	if err := s.enqueue(workItem{
		name: name,
		run: func(ctx context.Context) error {
			defer s.clearRunActive(runID)
			var err error
			err = s.pipeline.Run(ctx, req)
			if err != nil && webhook && req.DeliveryID != "" {
				s.releaseDelivery(deliveryKey)
			}
			return err
		},
	}); err != nil {
		s.clearRunActive(runID)
		if webhook && req.DeliveryID != "" {
			s.releaseDelivery(deliveryKey)
		}
		return requestReviewResult{RunID: runID, Status: "rejected", Reason: err.Error()}, err
	}
	return requestReviewResult{RunID: runID, Status: "enqueued"}, nil
}

func (s *Server) isRunActive(runID string) bool {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	return s.active != nil && s.active[runID]
}

func (s *Server) markRunActive(runID string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.active == nil {
		s.active = make(map[string]bool)
	}
	s.active[runID] = true
}

func (s *Server) clearRunActive(runID string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	delete(s.active, runID)
}

func requestRunID(req review.Request) string {
	project := strings.TrimSpace(firstNonEmptyString(req.ProjectID, req.Repository))
	change := strings.TrimSpace(firstNonEmptyString(req.ChangeID, strconv.Itoa(req.MRIID)))
	if project == "" || change == "" || change == "0" {
		return ""
	}
	return project + "!" + change
}
