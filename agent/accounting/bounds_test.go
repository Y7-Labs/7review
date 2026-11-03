package accounting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Y4NN777/7review/agent/llm"
	"github.com/Y4NN777/7review/agent/orchestrator"
)

var boundsTime = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func testRoute() RouteBound {
	return RouteBound{Provider: "qualified", Model: "pinned-model-20261001", Version: "contract-v1", Currency: "USD", EvidenceRef: "operator:provider-contract-1", ValidFrom: boundsTime.Add(-time.Hour), ValidUntil: boundsTime.Add(time.Hour), InputHardLimit: 100, OutputHardLimit: 40, InputMicroPerMillion: 11, OutputMicroPerMillion: 13, FixedMicro: 2, AllOutputBounded: true, MaxActiveMS: 50}
}
func digestBytes(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
func quoteIntent() orchestrator.DispatchIntent {
	payload, _ := json.Marshal(llm.LLMRequest{Model: testRoute().Model, SystemPrompt: "system", UserMessage: "user", MaxTokens: 20})
	return orchestrator.DispatchIntent{Provider: testRoute().Provider, Model: testRoute().Model, Payload: payload, RequestDigest: digestBytes(payload)}
}

func TestQuoteReservesHardInputLimitAndRoundsEachChargeUp(t *testing.T) {
	c, err := NewCatalog([]RouteBound{testRoute()})
	if err != nil {
		t.Fatal(err)
	}
	q, err := c.Quote(quoteIntent(), boundsTime)
	if err != nil || q.InputTokens != 100 || q.OutputTokens != 20 || q.MoneyMicro != 4 || q.RouteVersion != "contract-v1" || q.RequestDigest != quoteIntent().RequestDigest {
		t.Fatalf("unsafe quote: %#v %v", q, err)
	}
}

func TestBoundsDenyUnqualifiedOrMismatchedRequests(t *testing.T) {
	for _, name := range []string{"unknown route", "alias", "expired", "before validity", "tampered bytes", "wrong model payload", "tools unqualified", "excess output", "zero output", "overflow"} {
		t.Run(name, func(t *testing.T) {
			route := testRoute()
			intent := quoteIntent()
			now := boundsTime
			switch name {
			case "unknown route":
				intent.Provider = "other"
			case "alias":
				intent.Model = "auto"
			case "expired":
				now = route.ValidUntil
			case "before validity":
				now = route.ValidFrom.Add(-time.Nanosecond)
			case "tampered bytes":
				intent.Payload = append(intent.Payload, ' ')
			case "wrong model payload":
				intent.Model = "different"
				route.Model = "different"
			case "overflow":
				route.InputHardLimit = math.MaxInt64
				route.InputMicroPerMillion = math.MaxInt64
			default:
				var req llm.LLMRequest
				_ = json.Unmarshal(intent.Payload, &req)
				if name == "tools unqualified" {
					req.Tools = []llm.ToolDefinition{{Name: "custom"}}
				}
				if name == "excess output" {
					req.MaxTokens = 41
				}
				if name == "zero output" {
					req.MaxTokens = 0
				}
				intent.Payload, _ = json.Marshal(req)
				intent.RequestDigest = digestBytes(intent.Payload)
			}
			c, err := NewCatalog([]RouteBound{route})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Quote(intent, now); err == nil {
				t.Fatal("unqualified spend accepted")
			}
		})
	}
}

func TestCatalogRejectsInvalidAuthorityAndDetachesConfiguration(t *testing.T) {
	for _, name := range []string{"negative price", "missing evidence", "unbounded output", "zero input", "duplicate route", "empty version", "bad period", "empty currency"} {
		t.Run(name, func(t *testing.T) {
			r := testRoute()
			switch name {
			case "negative price":
				r.FixedMicro = -1
			case "missing evidence":
				r.EvidenceRef = ""
			case "unbounded output":
				r.AllOutputBounded = false
			case "zero input":
				r.InputHardLimit = 0
			case "empty version":
				r.Version = ""
			case "bad period":
				r.ValidUntil = r.ValidFrom
			case "empty currency":
				r.Currency = ""
			}
			routes := []RouteBound{r}
			if name == "duplicate route" {
				routes = append(routes, r)
			}
			if _, err := NewCatalog(routes); err == nil {
				t.Fatal("invalid qualification accepted")
			}
		})
	}
	routes := []RouteBound{testRoute()}
	c, err := NewCatalog(routes)
	if err != nil {
		t.Fatal(err)
	}
	routes[0].Model = "mutated"
	if _, err := c.Quote(quoteIntent(), boundsTime); err != nil {
		t.Fatal("catalog retained mutable authority")
	}
	intent := quoteIntent()
	intent.Payload = []byte(strings.Repeat("x", 1<<20+1))
	intent.RequestDigest = digestBytes(intent.Payload)
	if _, err := c.Quote(intent, boundsTime); err == nil {
		t.Fatal("oversized request accepted")
	}
}

func TestDecodeRouteBoundsRequiresClosedExplicitQualification(t *testing.T) {
	raw := []byte(`{"schema_version":1,"routes":[{"provider":"openai_compat","model":"pinned-model","version":"contract-2026-11","currency":"USD","evidence_ref":"operator:contract-42","valid_from":"2026-11-01T00:00:00Z","valid_until":"2026-12-01T00:00:00Z","input_hard_limit":100000,"output_hard_limit":4096,"input_micro_per_million":1000,"output_micro_per_million":2000,"fixed_micro":3,"all_output_bounded":true,"max_active_ms":30000,"native_tools_qualified":false}]}`)
	routes, err := DecodeRouteBounds(raw)
	if err != nil || len(routes) != 1 {
		t.Fatalf("explicit route qualification: %#v %v", routes, err)
	}
	route := routes[0]
	if route.Provider != "openai_compat" || route.Model != "pinned-model" || route.Version != "contract-2026-11" || route.EvidenceRef != "operator:contract-42" || route.InputHardLimit != 100000 || route.OutputHardLimit != 4096 || route.MaxActiveMS != 30000 || route.NativeToolsQualified {
		t.Fatalf("route qualification changed: %#v", route)
	}
	for _, invalid := range [][]byte{
		nil,
		[]byte(`{"schema_version":1,"routes":[]}`),
		[]byte(`{"schema_version":2,"routes":[{}]}`),
		[]byte(`{"schema_version":1,"schema_version":1,"routes":[{}]}`),
		[]byte(`{"schema_version":1,"routes":[{"provider":"p","provider":"q"}]}`),
		[]byte(`{"schema_version":1,"routes":[{"provider":"p"}],"extra":true}`),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
	} {
		if got, err := DecodeRouteBounds(invalid); err == nil || got != nil {
			t.Fatalf("unsafe route qualification accepted: %s %#v %v", invalid, got, err)
		}
	}
}
