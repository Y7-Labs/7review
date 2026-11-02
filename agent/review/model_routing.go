package review

import (
	"context"
	"errors"
	"strings"
)

type ModelRoute struct{ Provider, Model string }
type ModelRouteChain struct {
	Routes    []ModelRoute
	MaxTokens int
}
type ModelRouting struct {
	Version int
	Roles   map[string]ModelRouteChain
}

func (r *ModelRouting) Validate() error {
	if r == nil {
		return nil
	}
	if r.Version != 1 || len(r.Roles) == 0 || len(r.Roles) > 2 {
		return errors.New("unsupported model routing contract")
	}
	for role, chain := range r.Roles {
		if (role != "reasoner" && role != "formatter") || chain.MaxTokens <= 0 || len(chain.Routes) == 0 || len(chain.Routes) > 8 {
			return errors.New("invalid frozen model role")
		}
		for _, route := range chain.Routes {
			for _, s := range []string{route.Provider, route.Model} {
				if s == "" || len(s) > 256 || strings.TrimSpace(s) != s || strings.ContainsRune(s, 0) {
					return errors.New("invalid frozen model route")
				}
			}
		}
	}
	return nil
}

func (r *ModelRouting) Clone() *ModelRouting {
	if r == nil {
		return nil
	}
	out := &ModelRouting{Version: r.Version, Roles: map[string]ModelRouteChain{}}
	for key, chain := range r.Roles {
		chain.Routes = append([]ModelRoute(nil), chain.Routes...)
		out.Roles[key] = chain
	}
	return out
}

type modelRoutingKey struct{}

func WithModelRouting(ctx context.Context, routing *ModelRouting) context.Context {
	return context.WithValue(ctx, modelRoutingKey{}, routing.Clone())
}
