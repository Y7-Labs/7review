package accounting

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/orchestrator"
)

// RouteBound is trusted operator qualification, never repository policy. The
// input cap must be a provider-enforced billable limit, not an estimated count.
type RouteBound struct {
	Provider, Model, Version, Currency, EvidenceRef         string
	ValidFrom, ValidUntil                                   time.Time
	InputHardLimit, OutputHardLimit                         int64
	InputMicroPerMillion, OutputMicroPerMillion, FixedMicro int64
	AllOutputBounded                                        bool
	MaxActiveMS                                             int64
	NativeToolsQualified                                    bool
}

type Quote struct {
	Qualification                                      RouteBound
	QualificationDigest                                string
	InputTokens, OutputTokens, MoneyMicro              int64
	ActiveMS                                           int64
	RouteVersion, EvidenceRef, RequestDigest, Currency string
}

type Catalog struct{ routes []RouteBound }

type routeBoundDocument struct {
	SchemaVersion int              `json:"schema_version"`
	Routes        []routeBoundJSON `json:"routes"`
}

type routeBoundJSON struct {
	Provider              string    `json:"provider"`
	Model                 string    `json:"model"`
	Version               string    `json:"version"`
	Currency              string    `json:"currency"`
	EvidenceRef           string    `json:"evidence_ref"`
	ValidFrom             time.Time `json:"valid_from"`
	ValidUntil            time.Time `json:"valid_until"`
	InputHardLimit        int64     `json:"input_hard_limit"`
	OutputHardLimit       int64     `json:"output_hard_limit"`
	InputMicroPerMillion  int64     `json:"input_micro_per_million"`
	OutputMicroPerMillion int64     `json:"output_micro_per_million"`
	FixedMicro            int64     `json:"fixed_micro"`
	AllOutputBounded      bool      `json:"all_output_bounded"`
	MaxActiveMS           int64     `json:"max_active_ms"`
	NativeToolsQualified  bool      `json:"native_tools_qualified"`
}

func DecodeRouteBounds(data []byte) ([]RouteBound, error) {
	invalid := errors.New("invalid route qualification document")
	if len(data) == 0 || len(data) > 64<<10 || rejectDuplicateRouteJSON(data) != nil {
		return nil, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document routeBoundDocument
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF || document.SchemaVersion != 1 || len(document.Routes) == 0 || len(document.Routes) > 64 {
		return nil, invalid
	}
	routes := make([]RouteBound, 0, len(document.Routes))
	for _, item := range document.Routes {
		for _, value := range []string{item.Provider, item.Model, item.Version, item.Currency, item.EvidenceRef} {
			if value == "" || value != strings.TrimSpace(value) {
				return nil, invalid
			}
		}
		routes = append(routes, RouteBound{
			Provider: item.Provider, Model: item.Model, Version: item.Version, Currency: item.Currency, EvidenceRef: item.EvidenceRef,
			ValidFrom: item.ValidFrom, ValidUntil: item.ValidUntil, InputHardLimit: item.InputHardLimit, OutputHardLimit: item.OutputHardLimit,
			InputMicroPerMillion: item.InputMicroPerMillion, OutputMicroPerMillion: item.OutputMicroPerMillion, FixedMicro: item.FixedMicro,
			AllOutputBounded: item.AllOutputBounded, MaxActiveMS: item.MaxActiveMS, NativeToolsQualified: item.NativeToolsQualified,
		})
	}
	if _, err := NewCatalog(routes); err != nil {
		return nil, invalid
	}
	return routes, nil
}

func rejectDuplicateRouteJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var consume func() error
	consume = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok || seen[key] {
					return errors.New("duplicate or invalid object key")
				}
				seen[key] = true
				if err = consume(); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim('}') {
				return errors.New("invalid object")
			}
		case '[':
			for decoder.More() {
				if err = consume(); err != nil {
					return err
				}
			}
			token, err = decoder.Token()
			if err != nil || token != json.Delim(']') {
				return errors.New("invalid array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := consume(); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return errors.New("trailing JSON data")
	}
	return nil
}

func NewCatalog(routes []RouteBound) (*Catalog, error) {
	seen := map[string]bool{}
	for _, r := range routes {
		key := r.Provider + "\x00" + r.Model
		if strings.TrimSpace(r.Provider) == "" || strings.TrimSpace(r.Model) == "" || r.Version == "" || r.EvidenceRef == "" || r.Currency == "" || r.InputHardLimit <= 0 || r.OutputHardLimit <= 0 || r.InputMicroPerMillion < 0 || r.OutputMicroPerMillion < 0 || r.FixedMicro < 0 || r.MaxActiveMS <= 0 || r.MaxActiveMS > int64((1<<63-1)/time.Millisecond) || !r.AllOutputBounded || r.ValidFrom.IsZero() || !r.ValidUntil.After(r.ValidFrom) || seen[key] {
			return nil, errors.New("invalid or duplicate route qualification")
		}
		seen[key] = true
	}
	return &Catalog{routes: append([]RouteBound(nil), routes...)}, nil
}

func (c *Catalog) Quote(intent orchestrator.DispatchIntent, now time.Time) (Quote, error) {
	if c == nil || len(intent.Payload) > 1<<20 || len(intent.Payload) == 0 {
		return Quote{}, errors.New("missing qualification or oversized payload")
	}
	digest := sha256.Sum256(intent.Payload)
	if intent.RequestDigest != "sha256:"+hex.EncodeToString(digest[:]) {
		return Quote{}, errors.New("request digest does not bind payload")
	}
	var req llm.LLMRequest
	decoder := json.NewDecoder(bytes.NewReader(intent.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return Quote{}, fmt.Errorf("invalid model request: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Quote{}, errors.New("trailing request data")
	}
	for _, r := range c.routes {
		if r.Provider != intent.Provider || r.Model != intent.Model {
			continue
		}
		if now.Before(r.ValidFrom) || !now.Before(r.ValidUntil) || req.Model != r.Model || req.MaxTokens <= 0 || int64(req.MaxTokens) > r.OutputHardLimit || (len(req.Tools) > 0 && !r.NativeToolsQualified) {
			return Quote{}, errors.New("request outside qualified route contract")
		}
		input := roundedCharge(r.InputHardLimit, r.InputMicroPerMillion)
		output := roundedCharge(int64(req.MaxTokens), r.OutputMicroPerMillion)
		total := new(big.Int).Add(input, output)
		total.Add(total, big.NewInt(r.FixedMicro))
		if !total.IsInt64() {
			return Quote{}, errors.New("monetary upper bound overflows int64")
		}
		qualification, _ := json.Marshal(r)
		return Quote{Qualification: r, QualificationDigest: hash(qualification), InputTokens: r.InputHardLimit, OutputTokens: int64(req.MaxTokens), MoneyMicro: total.Int64(), ActiveMS: r.MaxActiveMS, RouteVersion: r.Version, EvidenceRef: r.EvidenceRef, RequestDigest: intent.RequestDigest, Currency: r.Currency}, nil
	}
	return Quote{}, errors.New("resolved provider/model has no qualified bound")
}

func roundedCharge(tokens, price int64) *big.Int {
	value := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(price))
	value.Add(value, big.NewInt(999999))
	return value.Quo(value, big.NewInt(1000000))
}
