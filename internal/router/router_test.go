package router

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/harsh-791/git-Projects/llm-gateway/internal/providers"
)

// fake returns errs[i] on call i, then succeeds.
type fake struct {
	name   string
	errs   []error
	models []string // req.Model seen on each call
}

func (f *fake) next(req providers.ChatRequest) error {
	f.models = append(f.models, req.Model)
	if n := len(f.models); n <= len(f.errs) {
		return f.errs[n-1]
	}
	return nil
}

func (f *fake) Chat(_ context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	if err := f.next(req); err != nil {
		return nil, err
	}
	return &providers.ChatResponse{Model: f.name}, nil
}

func (f *fake) ChatStream(_ context.Context, req providers.ChatRequest) (<-chan providers.StreamChunk, error) {
	if err := f.next(req); err != nil {
		return nil, err
	}
	ch := make(chan providers.StreamChunk)
	close(ch)
	return ch, nil
}

func (f *fake) Name() string               { return f.name }
func (f *fake) Ping(context.Context) error { return nil }

func status(code int) error { return &providers.StatusError{Code: code} }

func TestRetriesTransientErrorsThenSucceeds(t *testing.T) {
	primary := &fake{name: "a", errs: []error{status(503), status(429)}}
	r := New(primary, &fake{name: "b"})
	r.BaseBackoff = 0

	resp, err := r.Chat(context.Background(), providers.ChatRequest{Model: "m"})
	if err != nil || resp.Provider != "a" || len(primary.models) != 3 {
		t.Fatalf("resp=%+v err=%v calls=%d", resp, err, len(primary.models))
	}
}

func TestFallsBackOnClientErrorWithDefaultModel(t *testing.T) {
	primary := &fake{name: "a", errs: []error{status(400)}}
	fallback := &fake{name: "b"}
	r := New(primary, fallback)

	resp, err := r.Chat(context.Background(), providers.ChatRequest{Model: "a-only-model"})
	if err != nil || resp.Provider != "b" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	if len(primary.models) != 1 || !slices.Equal(fallback.models, []string{""}) {
		t.Fatalf("400 must not be retried and fallback must get default model: %v %v", primary.models, fallback.models)
	}
}

func TestAllProvidersFail(t *testing.T) {
	r := New(&fake{name: "a", errs: []error{errors.New("down")}}, &fake{name: "b", errs: []error{status(401)}})
	if _, err := r.Chat(context.Background(), providers.ChatRequest{}); err == nil {
		t.Fatal("expected error")
	}
}
