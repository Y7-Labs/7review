package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type SimpleXChannel struct{ cfg Config }

type simpleXEnvelope struct {
	CorrID string `json:"corrId"`
	Resp   struct {
		Type     string `json:"type"`
		Contacts []struct {
			ID   int64  `json:"contactId"`
			Name string `json:"localDisplayName"`
		} `json:"contacts"`
		ChatItems []struct {
			ChatInfo struct {
				Type    string `json:"type"`
				Contact struct {
					ID   int64  `json:"contactId"`
					Name string `json:"localDisplayName"`
				} `json:"contact"`
			} `json:"chatInfo"`
			ChatItem struct {
				Direction struct {
					Type string `json:"type"`
				} `json:"chatDir"`
				Meta struct {
					ID int64 `json:"itemId"`
				} `json:"meta"`
				Content struct {
					Type    string `json:"type"`
					Message struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"msgContent"`
				} `json:"content"`
			} `json:"chatItem"`
		} `json:"chatItems"`
	} `json:"resp"`
}

func NewSimpleXChannel(cfg Config) SimpleXChannel { return SimpleXChannel{cfg: cfg} }
func (c SimpleXChannel) Name() string             { return c.cfg.Name }
func (c SimpleXChannel) SendDraft(ctx context.Context, msg DraftMessage) (DeliveryReceipt, error) {
	return c.send(ctx, draftBody("7review draft ready", msg))
}
func (c SimpleXChannel) SendFinalConfirmation(ctx context.Context, msg FinalConfirmationMessage) error {
	_, err := c.send(ctx, "7review final published\nrun: "+msg.RunID+"\n\n"+strings.TrimSpace(msg.FinalReport))
	return err
}

func (c SimpleXChannel) send(ctx context.Context, text string) (DeliveryReceipt, error) {
	contact := Setting(c.cfg.Settings, "contact_id")
	if contact == "" {
		name := Setting(c.cfg.Settings, "contact_name")
		if name == "" {
			return DeliveryReceipt{}, fmt.Errorf("simplex requires contact_id or contact_name")
		}
		response, err := executeSimpleXCommand(ctx, simpleXURL(c.cfg), "contacts-"+strconv.FormatInt(time.Now().UnixNano(), 10), "/contacts")
		if err != nil {
			return DeliveryReceipt{}, err
		}
		if response.Resp.Type != "contactsList" {
			return DeliveryReceipt{}, fmt.Errorf("simplex could not list contacts")
		}
		for _, candidate := range response.Resp.Contacts {
			if candidate.Name == name {
				if contact != "" {
					return DeliveryReceipt{}, fmt.Errorf("simplex contact_name is ambiguous; configure contact_id")
				}
				contact = strconv.FormatInt(candidate.ID, 10)
			}
		}
		if contact == "" {
			return DeliveryReceipt{}, fmt.Errorf("simplex contact_name was not found")
		}
	}
	var cmd string
	if contact != "" {
		id, err := strconv.ParseInt(contact, 10, 64)
		if err != nil || id <= 0 {
			return DeliveryReceipt{}, fmt.Errorf("simplex contact_id must be a positive integer")
		}
		messages, err := json.Marshal([]any{map[string]any{
			"msgContent": map[string]string{"type": "text", "text": text},
			"mentions":   map[string]int64{},
		}})
		if err != nil {
			return DeliveryReceipt{}, err
		}
		cmd = "/_send @" + contact + " json " + string(messages)
	}
	corrID := "7review-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := writeSimpleXCommand(ctx, simpleXURL(c.cfg), corrID, cmd); err != nil {
		return DeliveryReceipt{}, err
	}
	return DeliveryReceipt{Channel: c.cfg.Name, ExternalID: corrID}, nil
}

func (c SimpleXChannel) Start(ctx context.Context, handler func(InboundMessage)) error {
	backoff := time.Second
	for ctx.Err() == nil {
		connected := time.Now()
		err := readSimpleXEvents(ctx, simpleXURL(c.cfg), func(data []byte) {
			for _, msg := range ParseSimpleXInbound(c.cfg.Name, data) {
				handler(msg)
			}
		})
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(connected) > time.Minute {
			backoff = time.Second
		}
		log.Printf("[channel:%s] simplex reconnecting: %v", c.cfg.Name, err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(2*backoff, 30*time.Second)
	}
	return nil
}

func ParseSimpleXInbound(name string, data []byte) []InboundMessage {
	var event simpleXEnvelope
	if json.Unmarshal(data, &event) != nil || event.CorrID != "" || event.Resp.Type != "newChatItems" {
		return nil
	}
	var messages []InboundMessage
	for _, item := range event.Resp.ChatItems {
		chat, content := item.ChatInfo, item.ChatItem.Content
		// Ignore sent-message acknowledgements and group messages: a group ID is not a sender.
		if chat.Type != "direct" || chat.Contact.ID <= 0 || item.ChatItem.Direction.Type != "directRcv" || content.Type != "rcvMsgContent" || content.Message.Type != "text" {
			continue
		}
		text := strings.TrimSpace(content.Message.Text)
		if text == "" {
			continue
		}
		messages = append(messages, InboundMessage{
			Channel: name, ExternalID: strconv.FormatInt(item.ChatItem.Meta.ID, 10),
			SenderID: strconv.FormatInt(chat.Contact.ID, 10), SenderAddress: chat.Contact.Name,
			RunID: RunIDFromCommand(text), Text: text, ReceivedAt: time.Now().UTC(),
		})
	}
	return messages
}

func simpleXURL(cfg Config) string {
	return firstNonEmpty(Setting(cfg.Settings, "ws_url"), "ws://127.0.0.1:5225")
}

func dialSimpleX(ctx context.Context, rawURL string) (*websocket.Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid simplex URL")
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return nil, fmt.Errorf("simplex requires ws or wss")
	}
	if u.Scheme == "ws" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return nil, fmt.Errorf("simplex plaintext WebSocket must use localhost; use wss with an authenticated proxy remotely")
		}
	}
	headers := http.Header{}
	if u.User != nil {
		password, _ := u.User.Password()
		req := &http.Request{Header: headers}
		req.SetBasicAuth(u.User.Username(), password)
		u.User = nil
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, u.String(), headers)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, fmt.Errorf("simplex WebSocket connection failed")
	}
	conn.SetReadLimit(1024 * 1024)
	return conn, nil
}

func writeSimpleXCommand(ctx context.Context, rawURL, corrID, cmd string) error {
	response, err := executeSimpleXCommand(ctx, rawURL, corrID, cmd)
	if err != nil {
		return err
	}
	if response.Resp.Type != "newChatItems" {
		return fmt.Errorf("simplex command rejected: %s", response.Resp.Type)
	}
	return nil
}

func executeSimpleXCommand(ctx context.Context, rawURL, corrID, cmd string) (simpleXEnvelope, error) {
	var response simpleXEnvelope
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := dialSimpleX(ctx, rawURL)
	if err != nil {
		return response, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetWriteDeadline(deadline)
	_ = conn.SetReadDeadline(deadline)
	if err := conn.WriteJSON(map[string]string{"corrId": corrID, "cmd": cmd}); err != nil {
		return response, fmt.Errorf("simplex write: %w", err)
	}
	for {
		response = simpleXEnvelope{}
		if err := conn.ReadJSON(&response); err != nil {
			return response, fmt.Errorf("simplex response: %w", err)
		}
		if response.CorrID != corrID {
			continue
		}
		return response, nil
	}
}

func readSimpleXEvents(ctx context.Context, rawURL string, handler func([]byte)) error {
	conn, err := dialSimpleX(ctx, rawURL)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if kind == websocket.TextMessage {
			handler(data)
		}
	}
}
