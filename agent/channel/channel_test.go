package channel

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestManagerVerifyInboundRequiresTokenAndAuthorizedSender(t *testing.T) {
	manager := NewManager([]Config{{
		Name:              "ops",
		Provider:          "log",
		Enabled:           true,
		InboundToken:      "secret",
		AuthorizedSenders: []string{"operator@example.com"},
	}})
	msg := InboundMessage{Channel: "ops", RunID: "owner/repo!7", SenderAddress: "operator@example.com", Text: "/approve owner/repo!7"}
	if _, err := manager.VerifyInbound("ops", "secret", msg); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.VerifyInbound("ops", "wrong", msg); err == nil {
		t.Fatal("expected invalid token error")
	}
	msg.SenderAddress = "attacker@example.com"
	if _, err := manager.VerifyInbound("ops", "secret", msg); err == nil {
		t.Fatal("expected unauthorized sender error")
	}
}

func TestEvaluateInboundOnlyApprovesExplicitRunScopedCommand(t *testing.T) {
	msg := InboundMessage{RunID: "owner/repo!7", SenderID: "operator", Text: "looks good"}
	result := EvaluateInbound(msg)
	if result.Approved || result.Reason == "" {
		t.Fatalf("ambiguous message should not approve: %#v", result)
	}
	msg.Text = "/approve owner/repo!8"
	result = EvaluateInbound(msg)
	if result.Approved {
		t.Fatalf("wrong run id should not approve: %#v", result)
	}
	msg.Text = "/approve owner/repo!7\napproved final text"
	result = EvaluateInbound(msg)
	if !result.Approved || result.FinalReport != "approved final text" {
		t.Fatalf("explicit approval not detected: %#v", result)
	}
}

func TestEvaluateInboundExtractsRunIDFromProviderText(t *testing.T) {
	msg := InboundMessage{Channel: "operator_whatsapp", SenderAddress: "whatsapp:+33600000000", Text: "/approve owner/repo!7"}
	result := EvaluateInbound(msg)
	if !result.Approved || result.RunID != "owner/repo!7" {
		t.Fatalf("provider command run id not extracted: %#v", result)
	}
}

func TestEvaluateInboundSupportsDraftRevisionAndSuppressionCommands(t *testing.T) {
	msg := InboundMessage{RunID: "owner/repo!7", SenderID: "operator", Text: "/revise owner/repo!7\nFocus on auth only"}
	result := EvaluateInbound(msg)
	if !result.Revised || result.Request != "Focus on auth only" || result.Approved {
		t.Fatalf("revision command not detected: %#v", result)
	}
	msg.Text = "/suppress owner/repo!7 F1\nFalse positive because middleware already checks this"
	result = EvaluateInbound(msg)
	if !result.Suppressed || result.FindingID != "F1" || result.Request == "" || result.Approved {
		t.Fatalf("suppression command not detected: %#v", result)
	}
}

func TestVerifyTwilioSignature(t *testing.T) {
	form := url.Values{}
	form.Set("From", "whatsapp:+33600000000")
	form.Set("To", "whatsapp:+14155238886")
	form.Set("Body", "/approve owner/repo!7")
	webhookURL := "https://7review.example.com/channels/twilio/whatsapp"
	authToken := "twilio-secret"
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(webhookURL + "Body" + form.Get("Body") + "From" + form.Get("From") + "To" + form.Get("To")))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !VerifyTwilioSignature(authToken, webhookURL, form, signature) {
		t.Fatal("expected valid twilio signature")
	}
	if VerifyTwilioSignature(authToken, webhookURL, form, "bad") {
		t.Fatal("expected invalid twilio signature")
	}
}

func TestParseTwilioWhatsAppInbound(t *testing.T) {
	form := url.Values{}
	form.Set("From", "whatsapp:+33600000000")
	form.Set("Body", "/revise owner/repo!7\nFocus on auth")
	form.Set("MessageSid", "SM123")
	msg := ParseTwilioWhatsAppInbound("operator_whatsapp", form)
	if msg.RunID != "owner/repo!7" || msg.SenderAddress != "whatsapp:+33600000000" || msg.ExternalID != "SM123" {
		t.Fatalf("twilio inbound not parsed: %#v", msg)
	}
}

func TestTelegramSendDraftCallsSendMessage(t *testing.T) {
	var gotPath string
	var gotPayload map[string]string
	ch := NewTelegramChannel(Config{
		Name:              "operator_telegram",
		AuthorizedSenders: []string{"12345"},
		Settings:          map[string]string{"bot_token": "bot-token", "chat_id": "12345", "api_base_url": "https://telegram.test"},
	})
	ch.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":42}}`)),
		}, nil
	})}

	receipt, err := ch.SendDraft(t.Context(), DraftMessage{RunID: "owner/repo!7", Repository: "owner/repo", ChangeID: "7", Summary: "summary"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/botbot-token/sendMessage" || gotPayload["chat_id"] != "12345" || !strings.Contains(gotPayload["text"], "/approve owner/repo!7") || receipt.ExternalID != "42" {
		t.Fatalf("telegram send mismatch path=%q payload=%#v receipt=%#v", gotPath, gotPayload, receipt)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestParseTelegramInboundAndSecret(t *testing.T) {
	update := TelegramUpdate{
		UpdateID: 99,
		Message:  &TelegramMessage{MessageID: 42, From: TelegramUser{ID: 12345, Username: "operator"}, Text: "/approve owner/repo!7"},
	}
	msg := ParseTelegramInbound("operator_telegram", update)
	if msg.RunID != "owner/repo!7" || msg.SenderID != "12345" || msg.SenderAddress != "operator" || msg.ExternalID != "99" {
		t.Fatalf("telegram inbound not parsed: %#v", msg)
	}
	if !VerifyTelegramWebhookSecret("secret", "secret") {
		t.Fatal("expected telegram secret to verify")
	}
	if VerifyTelegramWebhookSecret("secret", "bad") {
		t.Fatal("expected bad telegram secret to fail")
	}
}

func TestParseSimpleXInbound(t *testing.T) {
	data := []byte(`{"resp":{"type":"newChatItems","chatItems":[{"chatInfo":{"type":"direct","contact":{"contactId":1,"localDisplayName":"Operator"}},"chatItem":{"chatDir":{"type":"directRcv"},"meta":{"itemId":42},"content":{"type":"rcvMsgContent","msgContent":{"type":"text","text":"/revise owner/repo!7\nFocus on auth"}}}}]}}`)
	messages := ParseSimpleXInbound("operator_simplex", data)
	if len(messages) != 1 {
		t.Fatalf("expected one message: %#v", messages)
	}
	msg := messages[0]
	if msg.RunID != "owner/repo!7" || msg.SenderID != "1" || msg.SenderAddress != "Operator" || msg.ExternalID != "42" {
		t.Fatalf("simplex inbound not parsed: %#v", msg)
	}
}
