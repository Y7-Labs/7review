package channel

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

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
	RunID       string `json:"run_id"`
	Channel     string `json:"channel"`
	Sender      string `json:"sender"`
	Command     string `json:"command"`
	Approved    bool   `json:"approved"`
	Revised     bool   `json:"revised,omitempty"`
	Suppressed  bool   `json:"suppressed,omitempty"`
	FindingID   string `json:"finding_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
	FinalReport string `json:"final_report,omitempty"`
	Request     string `json:"request,omitempty"`
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
	if (cfg.Provider != "log" && len(allowed) == 0) || !senderAllowed(allowed, sender) {
		return Config{}, fmt.Errorf("sender is not authorized")
	}
	return cfg, nil
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
