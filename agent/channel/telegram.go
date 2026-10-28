package channel

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type TelegramChannel struct {
	cfg        Config
	httpClient *http.Client
}

type TelegramUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *TelegramMessage `json:"message,omitempty"`
	CallbackQuery *struct {
		ID      string           `json:"id"`
		From    TelegramUser     `json:"from"`
		Message *TelegramMessage `json:"message,omitempty"`
		Data    string           `json:"data,omitempty"`
	} `json:"callback_query,omitempty"`
}

type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	From      TelegramUser `json:"from"`
	Text      string       `json:"text,omitempty"`
}

type TelegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username,omitempty"`
}

func NewTelegramChannel(cfg Config) TelegramChannel {
	return TelegramChannel{cfg: cfg, httpClient: &http.Client{Timeout: 30 * time.Second}}
}

func (c TelegramChannel) Name() string {
	return c.cfg.Name
}

func (c TelegramChannel) SendDraft(ctx context.Context, msg DraftMessage) (DeliveryReceipt, error) {
	return c.sendMessage(ctx, draftBody("7review draft ready", msg))
}

func (c TelegramChannel) SendFinalConfirmation(ctx context.Context, msg FinalConfirmationMessage) error {
	_, err := c.sendMessage(ctx, "7review final published\nrun: "+msg.RunID+"\n\n"+strings.TrimSpace(msg.FinalReport))
	return err
}

func (c TelegramChannel) sendMessage(ctx context.Context, text string) (DeliveryReceipt, error) {
	// 2000 runes stay below Telegram's 4096-character cap even for UTF-16 pairs.
	runes := []rune(strings.TrimSpace(text))
	var receipt DeliveryReceipt
	for len(runes) > 0 {
		n := min(2000, len(runes))
		part, err := c.sendMessagePart(ctx, string(runes[:n]))
		if err != nil {
			return receipt, err
		}
		if receipt.ExternalID == "" {
			receipt = part
		}
		runes = runes[n:]
	}
	return receipt, nil
}

func (c TelegramChannel) sendMessagePart(ctx context.Context, text string) (DeliveryReceipt, error) {
	token := Setting(c.cfg.Settings, "bot_token")
	chatID := firstNonEmpty(Setting(c.cfg.Settings, "chat_id"), firstSender(c.cfg.AuthorizedSenders))
	if token == "" || chatID == "" {
		return DeliveryReceipt{}, fmt.Errorf("telegram channel %s missing bot_token/chat_id", c.cfg.Name)
	}
	payload, err := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return DeliveryReceipt{}, err
	}
	apiBase := firstNonEmpty(Setting(c.cfg.Settings, "api_base_url"), "https://api.telegram.org")
	endpoint := strings.TrimRight(apiBase, "/") + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return DeliveryReceipt{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return DeliveryReceipt{}, fmt.Errorf("telegram sendMessage request failed")
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DeliveryReceipt{}, fmt.Errorf("telegram send status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var parsed struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || !parsed.OK || parsed.Result.MessageID == 0 {
		return DeliveryReceipt{}, fmt.Errorf("telegram returned an invalid or unsuccessful sendMessage response")
	}
	return DeliveryReceipt{Channel: c.cfg.Name, ExternalID: strconv.FormatInt(parsed.Result.MessageID, 10)}, nil
}

func VerifyTelegramWebhookSecret(expected string, got string) bool {
	expected = strings.TrimSpace(expected)
	got = strings.TrimSpace(got)
	return expected != "" && got != "" && subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}

func ParseTelegramInbound(channelName string, update TelegramUpdate) InboundMessage {
	text := ""
	externalID := strconv.FormatInt(update.UpdateID, 10)
	var from TelegramUser
	if update.Message != nil {
		text = update.Message.Text
		from = update.Message.From
	}
	if update.CallbackQuery != nil {
		from = update.CallbackQuery.From
		if update.CallbackQuery.Data != "" {
			text = update.CallbackQuery.Data
		} else if update.CallbackQuery.Message != nil {
			text = update.CallbackQuery.Message.Text
		}
	}
	text = strings.TrimSpace(text)
	return InboundMessage{
		Channel:       channelName,
		ExternalID:    externalID,
		RunID:         RunIDFromCommand(text),
		SenderID:      strconv.FormatInt(from.ID, 10),
		SenderAddress: strings.TrimSpace(from.Username),
		Text:          text,
		ReceivedAt:    time.Now().UTC(),
	}
}
