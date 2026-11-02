package orchestrator

import (
	"errors"
	"github.com/Y4NN777/7review/agent/review"
)

func (o *Orchestrator) RoutingSnapshot() (*review.ModelRouting, error) {
	if o == nil || o.cfg == nil {
		return nil, errors.New("model routing unavailable")
	}
	out := &review.ModelRouting{Version: 1, Roles: map[string]review.ModelRouteChain{}}
	for _, role := range []ModelRole{RoleReasoner, RoleFormatter} {
		cfg, ok := o.cfg.Roles[role]
		if !ok {
			continue
		}
		chain := review.ModelRouteChain{MaxTokens: cfg.MaxTokens}
		for _, route := range append([]ModelSpec{cfg.Primary}, cfg.Fallbacks...) {
			chain.Routes = append(chain.Routes, review.ModelRoute{Provider: route.Provider, Model: route.Model})
		}
		out.Roles[string(role)] = chain
	}
	return out, out.Validate()
}
