package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Y4NN777/7review/agent/channel"
	"github.com/Y4NN777/7review/agent/pipeline"
	"github.com/Y4NN777/7review/agent/review"
)

func TestHandleChannelInboundRecordsMessageWithoutImplicitApproval(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	run, err := store.Start(context.Background(), review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs:     store,
			Channels: channel.NewManager([]channel.Config{{Name: "ops", Provider: "log", Enabled: true, InboundToken: "secret", AuthorizedSenders: []string{"operator"}}}),
		},
		work: make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/channels/ops/inbound", strings.NewReader(`{"run_id":"`+run.ID+`","sender_id":"operator","text":"looks good"}`))
	req.Header.Set("X-7Review-Channel-Token", "secret")
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 0 {
		t.Fatalf("ambiguous message should not enqueue approval")
	}
	stored, err := store.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !eventTypeSeen(stored.Events, "channel_message_received") {
		t.Fatalf("channel event not recorded: %#v", stored.Events)
	}
}

func TestHandleChannelInboundRejectsUnauthorizedSender(t *testing.T) {
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs:     pipeline.NewMemoryRunStore(),
			Channels: channel.NewManager([]channel.Config{{Name: "ops", Provider: "log", Enabled: true, InboundToken: "secret", AuthorizedSenders: []string{"operator"}}}),
		},
		work: make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/channels/ops/inbound", strings.NewReader(`{"run_id":"owner/repo!7","sender_id":"attacker","text":"/approve owner/repo!7"}`))
	req.Header.Set("X-7Review-Channel-Token", "secret")
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleChannelInboundEnqueuesExplicitApproval(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	run, err := store.Start(context.Background(), review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs:     store,
			Channels: channel.NewManager([]channel.Config{{Name: "ops", Provider: "log", Enabled: true, InboundToken: "secret", AuthorizedSenders: []string{"operator"}}}),
		},
		work: make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/channels/ops/inbound", strings.NewReader(`{"run_id":"`+run.ID+`","sender_id":"operator","text":"/approve `+run.ID+`"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 1 {
		t.Fatalf("explicit approval should enqueue approval work, queued=%d", len(s.work))
	}
}

func TestHandleChannelInboundEnqueuesDraftRevision(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	run, err := store.Start(context.Background(), review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs:     store,
			Channels: channel.NewManager([]channel.Config{{Name: "ops", Provider: "log", Enabled: true, InboundToken: "secret", AuthorizedSenders: []string{"operator"}}}),
		},
		work: make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/channels/ops/inbound", strings.NewReader(`{"run_id":"`+run.ID+`","sender_id":"operator","text":"/revise `+run.ID+`\nFocus on auth only"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 1 {
		t.Fatalf("revision should enqueue work, queued=%d", len(s.work))
	}
}

func TestHandleTwilioWhatsAppInboundEnqueuesExplicitApproval(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	run, err := store.Start(context.Background(), review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	webhookURL := "https://7review.example.com/channels/twilio/whatsapp"
	authToken := "twilio-secret"
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs: store,
			Channels: channel.NewManager([]channel.Config{{
				Name:              "operator_whatsapp",
				Provider:          "twilio_whatsapp",
				Enabled:           true,
				AuthorizedSenders: []string{"whatsapp:+33600000000"},
				Settings:          map[string]string{"auth_token": authToken, "webhook_url": webhookURL},
			}}),
		},
		work: make(chan workItem, 1),
	}
	form := url.Values{}
	form.Set("From", "whatsapp:+33600000000")
	form.Set("To", "whatsapp:+14155238886")
	form.Set("Body", "/approve "+run.ID)
	form.Set("MessageSid", "SM123")
	req := httptest.NewRequest(http.MethodPost, "/channels/twilio/whatsapp", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", signTwilio(authToken, webhookURL, form))
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 1 {
		t.Fatalf("twilio approval should enqueue work, queued=%d", len(s.work))
	}
}

func TestHandleTwilioWhatsAppInboundRejectsBadSignature(t *testing.T) {
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs: pipeline.NewMemoryRunStore(),
			Channels: channel.NewManager([]channel.Config{{
				Name:              "operator_whatsapp",
				Provider:          "twilio_whatsapp",
				Enabled:           true,
				AuthorizedSenders: []string{"whatsapp:+33600000000"},
				Settings:          map[string]string{"auth_token": "twilio-secret", "webhook_url": "https://7review.example.com/channels/twilio/whatsapp"},
			}}),
		},
		work: make(chan workItem, 1),
	}
	form := url.Values{}
	form.Set("From", "whatsapp:+33600000000")
	form.Set("Body", "/approve owner/repo!7")
	req := httptest.NewRequest(http.MethodPost, "/channels/twilio/whatsapp", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", "bad")
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleTelegramInboundEnqueuesRevision(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	run, err := store.Start(context.Background(), review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs: store,
			Channels: channel.NewManager([]channel.Config{{
				Name:              "operator_telegram",
				Provider:          "telegram",
				Enabled:           true,
				InboundToken:      "global-bridge-token",
				AuthorizedSenders: []string{"12345"},
				Settings:          map[string]string{"webhook_secret": "telegram-secret"},
			}}),
		},
		work: make(chan workItem, 1),
	}
	body := `{"update_id":99,"message":{"message_id":42,"from":{"id":12345,"username":"operator"},"text":"/revise ` + run.ID + `\nFocus on auth only"}}`
	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/webhook", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "telegram-secret")
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(s.work) != 1 {
		t.Fatalf("telegram revision should enqueue work, queued=%d", len(s.work))
	}
}

func TestChannelInbound_ProviderCannotUseGenericBridge(t *testing.T) {
	for _, provider := range []string{"telegram", "simplex", "twilio_whatsapp"} {
		t.Run(provider, func(t *testing.T) {
			s := &Server{pipeline: &pipeline.Pipeline{Jobs: pipeline.NewMemoryRunStore(), Channels: channel.NewManager([]channel.Config{
				{Name: "ops", Provider: provider, Enabled: true, AuthorizedSenders: []string{"123"}},
			})}, work: make(chan workItem, 1)}
			r := httptest.NewRequest(http.MethodPost, "/channels/ops/inbound", strings.NewReader(`{"sender_id":"123","run_id":"run","text":"/approve run"}`))
			w := httptest.NewRecorder()
			s.handleChannelInbound(w, r)
			if w.Code != http.StatusNotFound || len(s.work) != 0 {
				t.Fatalf("provider bypass: %d", w.Code)
			}
		})
	}
}

func TestSimpleX_ListenerRoutesAuthorizedRevision(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	run, err := store.Start(t.Context(), review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for _, sender := range []int{999, 1} {
			_ = conn.WriteJSON(map[string]any{"resp": map[string]any{"type": "newChatItems", "chatItems": []any{
				map[string]any{"chatInfo": map[string]any{"type": "direct", "contact": map[string]any{"contactId": sender, "localDisplayName": "Operator"}},
					"chatItem": map[string]any{"chatDir": map[string]string{"type": "directRcv"}, "meta": map[string]int{"itemId": sender}, "content": map[string]any{"type": "rcvMsgContent", "msgContent": map[string]string{"type": "text", "text": "/revise " + run.ID + "\nFocus on auth"}}}},
			}}})
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &Server{pipeline: &pipeline.Pipeline{Jobs: store, Channels: channel.NewManager([]channel.Config{
		{Name: "simplex", Provider: "simplex", Enabled: true, InboundToken: "global", Settings: map[string]string{"contact_id": "1", "ws_url": "ws" + strings.TrimPrefix(server.URL, "http")}},
	})}, work: make(chan workItem, 2)}
	s.startChannelListeners(ctx)
	select {
	case item := <-s.work:
		if item.name != "channel/revise/"+run.ID {
			t.Fatalf("wrong work: %s", item.name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not queue revision")
	}
	cancel()
	if len(s.work) != 0 {
		t.Fatal("unauthorized sender queued work")
	}
	stored, err := store.Get(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range stored.Events {
		if event.Type == "channel_message_received" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected only authorized event, got %d", count)
	}
}

func TestHandleTelegramInboundRejectsBadSecret(t *testing.T) {
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs: pipeline.NewMemoryRunStore(),
			Channels: channel.NewManager([]channel.Config{{
				Name:              "operator_telegram",
				Provider:          "telegram",
				Enabled:           true,
				AuthorizedSenders: []string{"12345"},
				Settings:          map[string]string{"webhook_secret": "telegram-secret"},
			}}),
		},
		work: make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/webhook", strings.NewReader(`{"update_id":99}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "bad")
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleTelegramInboundRejectsUnauthorizedSender(t *testing.T) {
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs: pipeline.NewMemoryRunStore(),
			Channels: channel.NewManager([]channel.Config{{
				Name:              "operator_telegram",
				Provider:          "telegram",
				Enabled:           true,
				AuthorizedSenders: []string{"12345"},
				Settings:          map[string]string{"webhook_secret": "telegram-secret"},
			}}),
		},
		work: make(chan workItem, 1),
	}
	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/webhook", strings.NewReader(`{"update_id":99,"message":{"from":{"id":999},"text":"/approve owner/repo!7"}}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "telegram-secret")
	rec := httptest.NewRecorder()

	s.handleChannelInbound(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSimpleXInboundMessageEnqueuesRevision(t *testing.T) {
	store := pipeline.NewMemoryRunStore()
	run, err := store.Start(context.Background(), review.Request{Provider: "github", ProjectID: "owner/repo", ChangeID: "7"})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		pipeline: &pipeline.Pipeline{
			Jobs: store,
			Channels: channel.NewManager([]channel.Config{{
				Name:              "operator_simplex",
				Provider:          "simplex",
				Enabled:           true,
				AuthorizedSenders: []string{"contact-1"},
			}}),
		},
		work: make(chan workItem, 1),
	}
	msg := channel.InboundMessage{Channel: "operator_simplex", RunID: run.ID, SenderID: "contact-1", Text: "/revise " + run.ID + "\nFocus on auth only"}
	if _, err := s.pipeline.Channels.VerifyInbound("operator_simplex", "", msg); err != nil {
		t.Fatal(err)
	}
	_, status, err := s.routeChannelMessageContext(context.Background(), msg)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("expected accepted simplex message, status=%d err=%v", status, err)
	}
	if len(s.work) != 1 {
		t.Fatalf("simplex revision should enqueue work, queued=%d", len(s.work))
	}
}

func signTwilio(authToken string, webhookURL string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for key := range form {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	base := webhookURL
	for _, key := range keys {
		base += key + form.Get(key)
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(base))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func eventTypeSeen(events []pipeline.RunEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
