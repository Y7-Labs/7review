package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/Y4NN777/7review/agent/review"
)

// Orchestrator is the multi-LLM coordinator.
// It knows nothing about code review logic — it only knows:
//   - which role maps to which provider chain
//   - how to fan out parallel calls and merge results
//   - how to walk the fallback chain on failure
type Orchestrator struct {
	cfg              *OrchestratorConfig
	providers        map[string]LLMProvider // keyed by provider name
	operatorCeilings *review.ResourceLimits
	budgetAuthority  BudgetAuthority
}

type RoleStatus struct {
	Role        string
	Primary     string
	Fallbacks   []string
	MaxTokens   int
	Parallel    bool
	MaxParallel int
}

func (o *Orchestrator) RoleStatus() []RoleStatus {
	if o == nil || o.cfg == nil {
		return nil
	}
	return roleStatusFromConfig(o.cfg)
}

func roleStatusFromConfig(cfg *OrchestratorConfig) []RoleStatus {
	if cfg == nil {
		return nil
	}
	roles := make([]string, 0, len(cfg.Roles))
	for role := range cfg.Roles {
		roles = append(roles, string(role))
	}
	sort.Strings(roles)
	out := make([]RoleStatus, 0, len(roles))
	for _, role := range roles {
		roleCfg := cfg.Roles[ModelRole(role)]
		fallbacks := make([]string, 0, len(roleCfg.Fallbacks))
		for _, fallback := range roleCfg.Fallbacks {
			fallbacks = append(fallbacks, formatModelSpec(fallback))
		}
		out = append(out, RoleStatus{
			Role:        role,
			Primary:     formatModelSpec(roleCfg.Primary),
			Fallbacks:   fallbacks,
			MaxTokens:   roleCfg.MaxTokens,
			Parallel:    roleCfg.Parallel,
			MaxParallel: roleCfg.MaxParallel,
		})
	}
	return out
}

func formatModelSpec(spec ModelSpec) string {
	if spec.Model == "" && spec.Provider == "" {
		return ""
	}
	return spec.Model + "@" + spec.Provider
}

func (o *Orchestrator) StreamComplete(
	ctx context.Context,
	rc *review.Context,
	role ModelRole,
	step string,
	systemPrompt, userMessage string,
	emit func(string) error,
) (string, error) {
	if rc != nil && !rc.Source.Policy.AllowsCapability("model.review") {
		return "", errors.New("orchestrator: effective policy denies model.review")
	}
	roleCfg, ok := o.cfg.Roles[role]
	if !ok {
		return "", fmt.Errorf("orchestrator: unknown role %q", role)
	}
	if emit == nil {
		return "", fmt.Errorf("orchestrator: stream emitter is required")
	}

	chain := append([]ModelSpec{roleCfg.Primary}, roleCfg.Fallbacks...)
	var lastErr error
	for _, spec := range chain {
		provider, ok := o.providers[spec.Provider]
		if !ok {
			log.Printf("[orchestrator] %s: provider %q not registered, skipping", step, spec.Provider)
			continue
		}

		var b strings.Builder
		streamReq := LLMRequest{
			Model:        spec.Model,
			SystemPrompt: systemPrompt,
			UserMessage:  userMessage,
			MaxTokens:    roleCfg.MaxTokens,
		}
		if streaming, ok := provider.(StreamingLLMProvider); ok {
			log.Printf("[orchestrator] %s: streaming %s/%s", step, spec.Provider, spec.Model)
			_, err := o.dispatchInvocation(ctx, rc, spec, role, step, streamReq, "stream", func(callCtx context.Context) (string, error) {
				err := streaming.Stream(callCtx, streamReq, func(delta string) error {
					b.WriteString(delta)
					return emit(delta)
				})
				return b.String(), err
			})
			if err != nil {
				if fatalDispatch(err) {
					return "", err
				}
				lastErr = err
				log.Printf("[orchestrator] %s: stream %s/%s failed: %v", step, spec.Provider, spec.Model, err)
				continue
			}
			rc.RecordProvider(step, fmt.Sprintf("%s/%s", spec.Provider, spec.Model))
			return b.String(), nil
		}

		log.Printf("[orchestrator] %s: provider %s/%s has no streaming API, using compatibility response", step, spec.Provider, spec.Model)
		result, err := o.dispatch(ctx, rc, spec, role, step, streamReq, func(callCtx context.Context) (string, error) { return provider.Complete(callCtx, streamReq) })
		if err != nil {
			if fatalDispatch(err) {
				return "", err
			}
			lastErr = err
			continue
		}
		if err := emit(result); err != nil {
			return "", err
		}
		rc.RecordProvider(step, fmt.Sprintf("%s/%s", spec.Provider, spec.Model))
		return result, nil
	}
	return "", fmt.Errorf("orchestrator: %s: all providers failed, last error: %w", step, lastErr)
}

// NewOrchestrator builds an Orchestrator from an OrchestratorConfig.
// All providers referenced in the config must be registered.
func NewOrchestrator(cfg *OrchestratorConfig, providers map[string]LLMProvider, options ...Option) *Orchestrator {
	o := &Orchestrator{cfg: cfg, providers: providers}
	for _, option := range options {
		option(o)
	}
	return o
}

// Complete sends a request under a given role.
// Tries Primary, then each Fallback in order until one succeeds.
// Records the winning provider into ctx (ReviewContext).
func (o *Orchestrator) Complete(
	ctx context.Context,
	rc *review.Context,
	role ModelRole,
	step string,
	systemPrompt, userMessage string,
	tools ...ToolDefinition,
) (string, error) {
	if rc != nil && !rc.Source.Policy.AllowsCapability("model.review") {
		return "", errors.New("orchestrator: effective policy denies model.review")
	}
	roleCfg, ok := o.cfg.Roles[role]
	if !ok {
		return "", fmt.Errorf("orchestrator: unknown role %q", role)
	}

	chain := append([]ModelSpec{roleCfg.Primary}, roleCfg.Fallbacks...)

	var lastErr error
	for _, spec := range chain {
		provider, ok := o.providers[spec.Provider]
		if !ok {
			log.Printf("[orchestrator] %s: provider %q not registered, skipping", step, spec.Provider)
			continue
		}

		log.Printf("[orchestrator] %s: trying %s/%s", step, spec.Provider, spec.Model)
		req := LLMRequest{
			Model:        spec.Model,
			SystemPrompt: systemPrompt,
			UserMessage:  userMessage,
			MaxTokens:    roleCfg.MaxTokens,
			Tools:        append([]ToolDefinition(nil), tools...),
		}
		result, err := o.dispatch(ctx, rc, spec, role, step, req, func(callCtx context.Context) (string, error) { return provider.Complete(callCtx, req) })
		if err != nil {
			if fatalDispatch(err) {
				return "", err
			}
			log.Printf("[orchestrator] %s: %s/%s failed: %v — trying fallback", step, spec.Provider, spec.Model, err)
			lastErr = err
			continue
		}

		rc.RecordProvider(step, fmt.Sprintf("%s/%s", spec.Provider, spec.Model))
		return result, nil
	}

	return "", fmt.Errorf("orchestrator: %s: all providers failed, last error: %w", step, lastErr)
}

// CompleteParallel fans out the review across multiple file batches,
// potentially routing batches to different providers concurrently.
//
// Routing strategy:
//   - Simple files (token count < threshold) → cheapest available provider
//   - Complex files (token count >= threshold) → primary reasoner
//
// All results are collected into rc.AddFindings() in a thread-safe way.
func (o *Orchestrator) CompleteParallel(
	ctx context.Context,
	rc *review.Context,
	systemPrompt string,
	batches [][]review.FileDiff,
	buildUserMsg func(batch []review.FileDiff) string,
	tools ...ToolDefinition,
) error {
	if rc != nil && !rc.Source.Policy.AllowsCapability("model.review") {
		return errors.New("orchestrator: effective policy denies model.review")
	}
	roleCfg, ok := o.cfg.Roles[RoleReasoner]
	if !ok {
		return errors.New("orchestrator: parallel: reasoner role not configured")
	}

	if !roleCfg.Parallel || len(batches) == 1 {
		// Serial fallback — either parallel disabled or only one batch.
		result, err := o.Complete(ctx, rc, RoleReasoner, "step5", systemPrompt, buildUserMsg(batches[0]), tools...)
		if err != nil {
			return err
		}
		rc.AddFindings(result)
		return nil
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)

	// Build the provider chain once — shared across goroutines (providers are stateless).
	chain := append([]ModelSpec{roleCfg.Primary}, roleCfg.Fallbacks...)
	maxParallel := roleCfg.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 4
	}
	if maxParallel > len(batches) {
		maxParallel = len(batches)
	}
	jobs := make(chan int)
	for worker := 0; worker < maxParallel; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batchIdx := range jobs {
				processBatch(ctx, o, rc, roleCfg, chain, systemPrompt, buildUserMsg, batchIdx, len(batches), batches[batchIdx], append([]ToolDefinition(nil), tools...), &mu, &firstErr)
			}
		}()
	}
	for i := range batches {
		jobs <- i
	}
	close(jobs)

	wg.Wait()
	return firstErr
}

func processBatch(
	ctx context.Context,
	o *Orchestrator,
	rc *review.Context,
	roleCfg RoleConfig,
	chain []ModelSpec,
	systemPrompt string,
	buildUserMsg func(batch []review.FileDiff) string,
	batchIdx int,
	batchCount int,
	batch []review.FileDiff,
	tools []ToolDefinition,
	mu *sync.Mutex,
	firstErr *error,
) {
	spec, provider, ok := o.pickRegisteredSpecForBatch(chain, batch, 2000)
	if !ok {
		mu.Lock()
		if *firstErr == nil {
			*firstErr = fmt.Errorf("batch %d: no registered provider in chain %s", batchIdx+1, describeProviderChain(chain))
		}
		mu.Unlock()
		return
	}

	userMsg := buildUserMsg(batch)
	log.Printf("[orchestrator] step5 batch %d/%d → %s/%s",
		batchIdx+1, batchCount, spec.Provider, spec.Model)

	req := LLMRequest{
		Model:        spec.Model,
		SystemPrompt: systemPrompt,
		UserMessage:  userMsg,
		MaxTokens:    roleCfg.MaxTokens,
		Tools:        append([]ToolDefinition(nil), tools...),
	}
	result, err := o.dispatch(ctx, rc, spec, RoleReasoner, fmt.Sprintf("step5_batch%d", batchIdx+1), req, func(callCtx context.Context) (string, error) { return provider.Complete(callCtx, req) })
	if err != nil {
		mu.Lock()
		if *firstErr == nil {
			*firstErr = fmt.Errorf("batch %d (%s/%s): %w", batchIdx+1, spec.Provider, spec.Model, err)
		}
		mu.Unlock()
		return
	}

	rc.AddFindings(result)
	rc.RecordProvider(
		fmt.Sprintf("step5_batch%d", batchIdx+1),
		fmt.Sprintf("%s/%s", spec.Provider, spec.Model),
	)
}

// pickRegisteredSpecForBatch selects a registered ModelSpec based on estimated
// token complexity. Complex batches use the first available provider in chain
// order. Simple batches prefer the last available fallback as the cheaper path.
func (o *Orchestrator) pickRegisteredSpecForBatch(chain []ModelSpec, batch []review.FileDiff, cheapThreshold int) (ModelSpec, LLMProvider, bool) {
	if len(chain) == 0 {
		return ModelSpec{}, nil, false
	}
	total := 0
	for _, f := range batch {
		total += f.TokenCount
	}
	if total < cheapThreshold && len(chain) > 1 {
		for i := len(chain) - 1; i >= 0; i-- {
			if provider, ok := o.providers[chain[i].Provider]; ok {
				return chain[i], provider, true
			}
		}
	}
	for _, spec := range chain {
		if provider, ok := o.providers[spec.Provider]; ok {
			return spec, provider, true
		}
	}
	return ModelSpec{}, nil, false
}
