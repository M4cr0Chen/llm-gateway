package router_test

import (
	"context"
	"errors"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// stubProvider is a no-op Provider whose Name() identifies it in tests.
type stubProvider struct{ name string }

func (s stubProvider) Name() string    { return s.name }
func (s stubProvider) Models() []string { return nil }
func (s stubProvider) ChatCompletion(context.Context, *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return nil, errors.New("not implemented")
}
func (s stubProvider) ChatCompletionStream(context.Context, *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	return nil, errors.New("not implemented")
}

// fixedStrategy implements router.Strategy and records the most recent
// candidate list it saw, so tests can verify what the Router passed in.
type fixedStrategy struct {
	name      string
	pickIndex int
	lastList  []router.Candidate
	lastMeta  router.RequestMeta
	err       error
}

func (f *fixedStrategy) Name() string { return f.name }
func (f *fixedStrategy) Select(candidates []router.Candidate, _ *model.ChatCompletionRequest, meta router.RequestMeta) (router.Candidate, error) {
	f.lastList = candidates
	f.lastMeta = meta
	if f.err != nil {
		return router.Candidate{}, f.err
	}
	return candidates[f.pickIndex], nil
}

func buildFixed(name string) func(string) (router.Strategy, error) {
	return func(string) (router.Strategy, error) {
		return &fixedStrategy{name: name}, nil
	}
}
