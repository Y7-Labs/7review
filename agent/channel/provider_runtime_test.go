package channel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCommands_RejectAmbiguousApproval(t *testing.T) {
	for _, text := range []string{"approve run", "approve run\nfinal", "/approve run maybe", "/approve"} {
		if got := EvaluateInbound(InboundMessage{RunID: "run", Text: text}); got.Approved {
			t.Fatalf("approved %q", text)
		}
	}
}

func TestManager_ProviderAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      Config
		msg      InboundMessage
		accepted bool
	}{
		{"telegram ignores bridge token", Config{Provider: "telegram", InboundToken: "global", AuthorizedSenders: []string{"123"}}, InboundMessage{SenderID: "123"}, true},
		{"telegram numeric identity only", Config{Provider: "telegram", AuthorizedSenders: []string{"123"}}, InboundMessage{SenderID: "999", SenderAddress: "123"}, false},
		{"telegram no allowlist", Config{Provider: "telegram"}, InboundMessage{SenderID: "123"}, false},
		{"simplex configured operator", Config{Provider: "simplex", Settings: map[string]string{"contact_id": "1"}}, InboundMessage{SenderID: "1"}, true},
		{"simplex rejects stranger", Config{Provider: "simplex", Settings: map[string]string{"contact_id": "1"}}, InboundMessage{SenderID: "2"}, false},
		{"log missing token", Config{Provider: "log"}, InboundMessage{SenderID: "operator"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Name = "ops"
			tc.cfg.Enabled = true
			m := NewManager([]Config{tc.cfg})
			_, err := m.VerifyInbound("ops", "", tc.msg)
			if (err == nil) != tc.accepted {
				t.Fatalf("accepted=%v err=%v", tc.accepted, err)
			}
		})
	}
}

func TestSimpleX_SendAcknowledgement(t *testing.T) {
	for _, responseType := range []string{"newChatItems", "chatCmdError"} {
		t.Run(responseType, func(t *testing.T) {
			commands := make(chan map[string]string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				var cmd map[string]string
				if conn.ReadJSON(&cmd) != nil {
					return
				}
				commands <- cmd
				_ = conn.WriteControl(websocket.PingMessage, []byte("keepalive"), time.Now().Add(time.Second))
				_ = conn.WriteJSON(map[string]any{"corrId": "unrelated", "resp": map[string]string{"type": "chatCmdError"}})
				_ = conn.WriteJSON(map[string]any{"corrId": cmd["corrId"], "resp": map[string]string{"type": responseType}})
			}))
			defer server.Close()
			ch := NewSimpleXChannel(Config{Name: "ops", Settings: map[string]string{"contact_id": "42", "ws_url": "ws" + strings.TrimPrefix(server.URL, "http")}})
			receipt, err := ch.SendDraft(t.Context(), DraftMessage{RunID: "run", DraftReport: "quoted \"text\"\nnext line"})
			if (err == nil) != (responseType == "newChatItems") {
				t.Fatalf("wrong send result: %v", err)
			}
			cmd := <-commands
			if cmd["corrId"] == "" || !strings.HasPrefix(cmd["cmd"], "/_send @42 json ") {
				t.Fatalf("bad command: %#v", cmd)
			}
			var payload []struct {
				Message struct {
					Text string `json:"text"`
				} `json:"msgContent"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(cmd["cmd"], "/_send @42 json ")), &payload); err != nil || len(payload) != 1 || !strings.Contains(payload[0].Message.Text, "quoted \"text\"\nnext line") {
				t.Fatalf("bad message: %#v %v", payload, err)
			}
			if err == nil && receipt.ExternalID != cmd["corrId"] {
				t.Fatal("receipt lost correlation")
			}
		})
	}
}

func TestSimpleX_ResolveContactName(t *testing.T) {
	commands := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var cmd map[string]string
		if conn.ReadJSON(&cmd) != nil {
			return
		}
		commands <- cmd["cmd"]
		if cmd["cmd"] == "/contacts" {
			_ = conn.WriteJSON(map[string]any{"corrId": cmd["corrId"], "resp": map[string]any{"type": "contactsList", "contacts": []any{map[string]any{"contactId": 7, "localDisplayName": "Operator"}}}})
		} else {
			_ = conn.WriteJSON(map[string]any{"corrId": cmd["corrId"], "resp": map[string]string{"type": "newChatItems"}})
		}
	}))
	defer server.Close()
	ch := NewSimpleXChannel(Config{Settings: map[string]string{"ws_url": "ws" + strings.TrimPrefix(server.URL, "http"), "contact_name": "Operator"}})
	if _, err := ch.SendDraft(t.Context(), DraftMessage{RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	if <-commands != "/contacts" || !strings.HasPrefix(<-commands, "/_send @7 json ") {
		t.Fatal("name did not resolve to numeric API ref")
	}
}

func TestSimpleX_ListenerCancellation(t *testing.T) {
	connected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		close(connected)
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- readSimpleXEvents(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), func([]byte) {}) }()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("did not connect")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle reader ignored cancellation")
	}
}

func TestSimpleX_RejectSentAndGroupEvents(t *testing.T) {
	for _, variant := range []string{
		`{"corrId":"send","resp":{"type":"newChatItems","chatItems":[]}}`,
		`{"resp":{"type":"newChatItems","chatItems":[{"chatInfo":{"type":"direct","contact":{"contactId":1}},"chatItem":{"chatDir":{"type":"directSnd"},"content":{"type":"sndMsgContent","msgContent":{"type":"text","text":"/approve run"}}}}]}}`,
		`{"resp":{"type":"newChatItems","chatItems":[{"chatInfo":{"type":"group","contact":{"contactId":1}},"chatItem":{"chatDir":{"type":"groupRcv"},"content":{"type":"rcvMsgContent","msgContent":{"type":"text","text":"/approve run"}}}}]}}`,
	} {
		if len(ParseSimpleXInbound("ops", []byte(variant))) != 0 {
			t.Fatal("accepted a non-operator event")
		}
	}
}

func TestTelegram_RejectInvalidResponses(t *testing.T) {
	for _, body := range []string{`{"ok":false}`, `not-json`, `{"ok":true,"result":{}}`} {
		ch := NewTelegramChannel(Config{Settings: map[string]string{"bot_token": "secret", "chat_id": "1"}})
		ch.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		if _, err := ch.SendDraft(t.Context(), DraftMessage{RunID: "run"}); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestTelegram_LongReportsPreserveText(t *testing.T) {
	ch := NewTelegramChannel(Config{Settings: map[string]string{"bot_token": "secret", "chat_id": "1"}})
	var parts []string
	ch.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		parts = append(parts, payload["text"])
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":1}}`))}, nil
	})}
	text := strings.Repeat("text\n", 1800)
	if _, err := ch.sendMessage(t.Context(), text); err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 || strings.Join(parts, "") != strings.TrimSpace(text) {
		t.Fatal("long report was lost or changed")
	}
}
