package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

var operatorTokenEnvPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

type Config struct {
	Name              string
	Provider          string
	Enabled           bool
	InboundToken      string
	AuthorizedSenders []string
	Settings          map[string]string
}

type DraftMessage struct {
	RunID       string
	Provider    string
	Repository  string
	ChangeID    string
	WebURL      string
	Summary     string
	DraftReport string
}

type FinalConfirmationMessage struct {
	RunID       string
	FinalReport string
}

type DeliveryReceipt struct {
	Channel    string `json:"channel"`
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
}

type InboundMessage struct {
	Channel       string    `json:"channel,omitempty"`
	ExternalID    string    `json:"external_id,omitempty"`
	RunID         string    `json:"run_id"`
	SenderID      string    `json:"sender_id,omitempty"`
	SenderAddress string    `json:"sender_address,omitempty"`
	Text          string    `json:"text"`
	ReceivedAt    time.Time `json:"received_at,omitempty"`
}

type InboundResult struct {
	RunID         string `json:"run_id"`
	Channel       string `json:"channel"`
	Sender        string `json:"sender"`
	Command       string `json:"command"`
	Approved      bool   `json:"approved"`
	Revised       bool   `json:"revised,omitempty"`
	Suppressed    bool   `json:"suppressed,omitempty"`
	FindingID     string `json:"finding_id,omitempty"`
	Reason        string `json:"reason,omitempty"`
	FinalReport   string `json:"final_report,omitempty"`
	Request       string `json:"request,omitempty"`
	AttemptID     string `json:"attempt_id,omitempty"`
	CommandID     string `json:"command_id,omitempty"`
	Decision      string `json:"decision,omitempty"`
	Dispositioned bool   `json:"dispositioned,omitempty"`
}

func (m *Manager) OperatorTokenEnv(channelName string, msg InboundMessage) (string, error) {
	if m == nil {
		return "", fmt.Errorf("channels are not configured")
	}
	cfg, ok := m.configs[normalizeName(firstNonEmpty(channelName, msg.Channel))]
	if !ok {
		return "", fmt.Errorf("channel is not configured")
	}
	if !authorizedChannelSender(cfg, msg) {
		return "", fmt.Errorf("sender is not authorized")
	}
	raw := Setting(cfg.Settings, "operator_token_env_by_sender")
	bindings, err := decodeOperatorTokenEnvBindings(raw)
	if err != nil {
		return "", err
	}
	matches := map[string]bool{}
	for _, sender := range []string{strings.TrimSpace(msg.SenderID), strings.TrimSpace(msg.SenderAddress)} {
		if sender == "" {
			continue
		}
		for configured, envName := range bindings {
			if strings.EqualFold(configured, sender) {
				matches[envName] = true
			}
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("channel sender has no unambiguous operator binding")
	}
	for envName := range matches {
		return envName, nil
	}
	return "", fmt.Errorf("channel sender has no operator binding")
}

func decodeOperatorTokenEnvBindings(data string) (map[string]string, error) {
	if len(data) == 0 || len(data) > 16<<10 {
		return nil, fmt.Errorf("operator sender bindings are required")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("invalid operator sender bindings")
	}
	out := map[string]string{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || strings.TrimSpace(key) != key || key == "" || len(key) > 256 {
			return nil, fmt.Errorf("invalid operator sender binding")
		}
		if _, exists := out[strings.ToLower(key)]; exists {
			return nil, fmt.Errorf("duplicate operator sender binding")
		}
		var envName string
		if err := decoder.Decode(&envName); err != nil || !operatorTokenEnvPattern.MatchString(envName) {
			return nil, fmt.Errorf("invalid operator token environment binding")
		}
		out[strings.ToLower(key)] = envName
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') || len(out) == 0 {
		return nil, fmt.Errorf("invalid operator sender bindings")
	}
	if token, err = decoder.Token(); err != io.EOF || token != nil {
		return nil, fmt.Errorf("invalid operator sender bindings")
	}
	return out, nil
}

type NotificationChannel interface {
	Name() string
	SendDraft(context.Context, DraftMessage) (DeliveryReceipt, error)
	SendFinalConfirmation(context.Context, FinalConfirmationMessage) error
}

type ListenerChannel interface {
	Start(context.Context, func(InboundMessage)) error
}

type Manager struct {
	configs   map[string]Config
	channels  map[string]NotificationChannel
	listeners map[string]ListenerChannel
	startOnce sync.Once
}

func NewManager(configs []Config) *Manager {
	m := &Manager{
		configs:   make(map[string]Config),
		channels:  make(map[string]NotificationChannel),
		listeners: make(map[string]ListenerChannel),
	}
	for _, cfg := range configs {
		if !cfg.Enabled {
			continue
		}
		name := normalizeName(firstNonEmpty(cfg.Name, cfg.Provider))
		if name == "" {
			continue
		}
		cfg.Name = name
		cfg.Provider = normalizeName(firstNonEmpty(cfg.Provider, "log"))
		cfg.AuthorizedSenders = cleanSenders(cfg.AuthorizedSenders)
		delete(m.listeners, name)
		switch cfg.Provider {
		case "twilio_whatsapp":
			m.channels[name] = NewTwilioWhatsAppChannel(cfg)
		case "telegram":
			m.channels[name] = NewTelegramChannel(cfg)
		case "simplex":
			ch := NewSimpleXChannel(cfg)
			m.channels[name] = ch
			m.listeners[name] = ch
		case "log":
			m.channels[name] = NewLogChannel(name)
		default:
			continue
		}
		m.configs[name] = cfg
	}
	return m
}

func (m *Manager) Enabled() bool {
	return m != nil && len(m.channels) > 0
}

func (m *Manager) SendDraft(ctx context.Context, msg DraftMessage) ([]DeliveryReceipt, error) {
	if m == nil {
		return nil, nil
	}
	var receipts []DeliveryReceipt
	for name, ch := range m.channels {
		receipt, err := ch.SendDraft(ctx, msg)
		if err != nil {
			return receipts, fmt.Errorf("channel %s send draft: %w", name, err)
		}
		if receipt.Channel == "" {
			receipt.Channel = name
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

func (m *Manager) SendFinalConfirmation(ctx context.Context, msg FinalConfirmationMessage) error {
	if m == nil {
		return nil
	}
	for name, ch := range m.channels {
		if err := ch.SendFinalConfirmation(ctx, msg); err != nil {
			return fmt.Errorf("channel %s final confirmation: %w", name, err)
		}
	}
	return nil
}

func (m *Manager) Start(ctx context.Context, handler func(InboundMessage)) error {
	if m == nil || len(m.listeners) == 0 {
		return nil
	}
	m.startOnce.Do(func() {
		for name, listener := range m.listeners {
			name := name
			listener := listener
			go func() {
				if err := listener.Start(ctx, handler); err != nil && ctx.Err() == nil {
					log.Printf("[channel:%s] listener stopped: %v", name, err)
				}
			}()
		}
	})
	return nil
}

func (m *Manager) VerifyInbound(channelName string, token string, msg InboundMessage) (Config, error) {
	if m == nil {
		return Config{}, fmt.Errorf("channels are not configured")
	}
	channelName = normalizeName(firstNonEmpty(channelName, msg.Channel))
	cfg, ok := m.configs[channelName]
	if !ok {
		return Config{}, fmt.Errorf("channel %q is not configured", channelName)
	}
	if cfg.Provider == "log" && (strings.TrimSpace(cfg.InboundToken) == "" || strings.TrimSpace(token) != strings.TrimSpace(cfg.InboundToken)) {
		return Config{}, fmt.Errorf("invalid channel token")
	}
	if !authorizedChannelSender(cfg, msg) {
		return Config{}, fmt.Errorf("sender is not authorized")
	}
	return cfg, nil
}

func authorizedChannelSender(cfg Config, msg InboundMessage) bool {
	allowed := cfg.AuthorizedSenders
	if cfg.Provider == "simplex" && len(allowed) == 0 {
		allowed = cleanSenders([]string{firstNonEmpty(Setting(cfg.Settings, "contact_id"), Setting(cfg.Settings, "contact_name"))})
	}
	sender := msg
	if cfg.Provider == "telegram" {
		sender.SenderAddress = ""
	}
	if cfg.Provider == "simplex" && Setting(cfg.Settings, "contact_id") != "" && len(cfg.AuthorizedSenders) == 0 {
		sender.SenderAddress = ""
	}
	return (cfg.Provider == "log" || len(allowed) > 0) && senderAllowed(allowed, sender)
}

// AllowsGenericInbound limits the JSON bridge to explicitly authenticated log channels.
func (m *Manager) AllowsGenericInbound(name string) bool {
	if m == nil {
		return false
	}
	cfg, ok := m.configs[normalizeName(name)]
	return ok && cfg.Provider == "log" && strings.TrimSpace(cfg.InboundToken) != ""
}

func (m *Manager) ConfigForProvider(provider string) (Config, bool) {
	if m == nil {
		return Config{}, false
	}
	provider = normalizeName(provider)
	for _, cfg := range m.configs {
		if normalizeName(cfg.Provider) == provider {
			return cfg, true
		}
	}
	return Config{}, false
}

func Setting(settings map[string]string, key string) string {
	if settings == nil {
		return ""
	}
	return strings.TrimSpace(settings[key])
}
