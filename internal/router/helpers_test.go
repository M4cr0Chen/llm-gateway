package router_test

import (
	"context"
	"sync/atomic"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// stubProvider is a Provider that succeeds quietly. It exists for the
// M4.1 tests that only care about candidate resolution and strategy
// selection — they don't drive the fallback loop, so the actual call
// just returns a zero-valued response. Tests that need failure
// injection or stream chunks should use scriptedProvider below.
type stubProvider struct{ name string }

func (s stubProvider) Name() string    { return s.name }
func (s stubProvider) Models() []string { return nil }
func (s stubProvider) ChatCompletion(context.Context, *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return &model.ChatCompletionResponse{}, nil
}
func (s stubProvider) ChatCompletionStream(context.Context, *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

// scriptedProvider returns canned responses for the M4.2 fallback tests.
// chatErr / streamErr drive the failure path; resp / chunks drive the
// success path. callCount is incremented atomically so tests can assert
// how many attempts hit a given provider.
type scriptedProvider struct {
	name      string
	resp      *model.ChatCompletionResponse
	chunks    []model.ChatCompletionChunk
	chatErr   error
	streamErr error
	callCount atomic.Int32
}

func (s *scriptedProvider) Name() string    { return s.name }
func (s *scriptedProvider) Models() []string { return nil }
func (s *scriptedProvider) ChatCompletion(context.Context, *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	s.callCount.Add(1)
	if s.chatErr != nil {
		return nil, s.chatErr
	}
	return s.resp, nil
}
func (s *scriptedProvider) ChatCompletionStream(context.Context, *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	s.callCount.Add(1)
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	ch := make(chan provider.StreamEvent, len(s.chunks))
	for i := range s.chunks {
		ch <- provider.StreamEvent{Chunk: &s.chunks[i]}
	}
	close(ch)
	return ch, nil
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
