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
