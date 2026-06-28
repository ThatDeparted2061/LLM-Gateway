// Package router sends requests to an ordered list of providers, retrying
// transient failures (429/5xx) with exponential backoff before falling back to
// the next provider.
package router

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

type Router struct {
	providers   []providers.Provider
	MaxRetries  int           // extra attempts per provider on 429/5xx
	BaseBackoff time.Duration // first retry delay; doubles per retry, plus up to 50% jitter
}

// New routes to ps in order: ps[0] is the primary, the rest are fallbacks.
func New(ps ...providers.Provider) *Router {
	return &Router{providers: ps, MaxRetries: 2, BaseBackoff: 250 * time.Millisecond}
}

func (r *Router) Providers() []providers.Provider { return r.providers }

// Chat returns the first successful completion.
func (r *Router) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	resp, name, err := try(ctx, r, req, providers.Provider.Chat)
	if err != nil {
		return nil, err
	}
	resp.Provider = name
	return resp, nil
}

// ChatStream returns the first provider stream that opens, plus its provider
// name. Failover only happens before the first byte; once a stream is flowing,
// errors arrive on the channel.
func (r *Router) ChatStream(ctx context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, string, error) {
	return try(ctx, r, req, providers.Provider.ChatStream)
}

// try calls each provider in turn. Non-retryable errors (4xx, network) move
// straight to the next provider; 429/5xx are retried with backoff first.
// ponytail: ignores upstream Retry-After; honor it if providers start sending long ones.
func try[T any](ctx context.Context, r *Router, req providers.ChatRequest,
	call func(providers.Provider, context.Context, providers.ChatRequest) (T, error)) (T, string, error) {
	var zero T
	var errs []error
	for i, p := range r.providers {
		if i > 0 {
			req.Model = "" // model names are provider-specific: fallbacks use their configured default
		}
		for attempt := 0; ; attempt++ {
			out, err := call(p, ctx, req)
			if err == nil {
				return out, p.Name(), nil
			}
			if ctx.Err() != nil {
				return zero, "", ctx.Err()
			}
			errs = append(errs, err)
			if !retryable(err) || attempt == r.MaxRetries {
				break
			}
			if err := sleep(ctx, r.backoff(attempt)); err != nil {
				return zero, "", err
			}
		}
	}
	return zero, "", fmt.Errorf("all providers failed: %w", errors.Join(errs...))
}

func retryable(err error) bool {
	var se *providers.StatusError
	return errors.As(err, &se) && se.Retryable()
}

func (r *Router) backoff(attempt int) time.Duration {
	d := r.BaseBackoff << attempt
	if d <= 0 {
		return 0
	}
	return d + rand.N(d/2+1)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
