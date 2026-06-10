package router

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// stub is a quiet-success Provider for internal tests — same intent as
// helpers_test.go's stubProvider, re-declared here because that file
// lives in the router_test package and isn't visible from package
// router. ChatCompletion returns an empty response so the M4.2 Route
// loop completes the call cleanly.
type stub struct{ name string }

func (s stub) Name() string    { return s.name }
func (s stub) Models() []string { return nil }
func (s stub) ChatCompletion(context.Context, *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return &model.ChatCompletionResponse{}, nil
}
func (s stub) ChatCompletionStream(context.Context, *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

type recordingStrategy struct{ pick int }

func (recordingStrategy) Name() string { return "priority" }
func (r recordingStrategy) Select(c []Candidate, _ *model.ChatCompletionRequest, _ RequestMeta) (Candidate, error) {
	if len(c) == 0 {
		return Candidate{}, errors.New("empty")
	}
	return c[r.pick], nil
}

// TestExpandGroup_WarnsOncePerUnresolvableCandidate covers the
// observability fix to expandGroup: a configured candidate whose Model
// isn't in the registry used to be silently dropped (surfaced as a
// confusing 503 all_providers_down). It must now emit exactly one WARN
// per distinct (provider, model) pair across the process lifetime.
func TestExpandGroup_WarnsOncePerUnresolvableCandidate(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(stub{name: "openai"}, []string{"gpt-4o"}) // anthropic intentionally not registered

	cfg := config.RoutingConfig{
		DefaultStrategy: "priority",
		ModelGroups: map[string]config.ModelGroupConfig{
			"smart": {
				Candidates: []config.CandidateConfig{
					{Provider: "openai", Model: "gpt-4o"},
					{Provider: "anthropic", Model: "claude-missing"},
				},
			},
		},
	}
	providers := map[string]config.ProviderConfig{"openai": {}, "anthropic": {}}

	build := func(string) (Strategy, error) { return recordingStrategy{}, nil }
	rtr, err := NewRouter(reg, cfg, providers, build)
	require.NoError(t, err)

	// Swap the WARN sink with a counter so we don't depend on slog
	// global state. The cast is safe because NewRouter returns
	// *defaultRouter (it just narrows to the Router interface).
	dr := rtr.(*defaultRouter)
	var warnCount atomic.Int64
	var lastProvider, lastModel string
	dr.skipWarn = func(p, m string) {
		warnCount.Add(1)
		lastProvider, lastModel = p, m
	}

	req := &model.ChatCompletionRequest{Model: "smart", Messages: []model.Message{{Role: "user", Content: "hi"}}}

	// Three Route calls — the second and third must NOT re-warn the
	// (anthropic, claude-missing) pair.
	for range 3 {
		_, _, err := rtr.Route(context.Background(), req, RequestMeta{})
		require.NoError(t, err, "the resolvable candidate keeps the request alive")
	}

	assert.Equal(t, int64(1), warnCount.Load(), "WARN must fire exactly once per (provider, model)")
	assert.Equal(t, "anthropic", lastProvider)
	assert.Equal(t, "claude-missing", lastModel)
}

// TestExpandGroup_DifferentPairsEachWarn confirms the warn-once key is
// (provider, model) rather than just provider or just model — so two
// distinct unresolvable pairs each surface.
func TestExpandGroup_DifferentPairsEachWarn(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(stub{name: "openai"}, []string{"gpt-4o"})

	cfg := config.RoutingConfig{
		DefaultStrategy: "priority",
		ModelGroups: map[string]config.ModelGroupConfig{
			"smart": {
				Candidates: []config.CandidateConfig{
					{Provider: "openai", Model: "gpt-4o"},
					{Provider: "anthropic", Model: "claude-missing"},
					{Provider: "google", Model: "gemini-missing"},
				},
			},
		},
	}
	providers := map[string]config.ProviderConfig{"openai": {}, "anthropic": {}, "google": {}}

	rtr, err := NewRouter(reg, cfg, providers, func(string) (Strategy, error) { return recordingStrategy{}, nil })
	require.NoError(t, err)

	dr := rtr.(*defaultRouter)
	seen := map[string]int{}
	dr.skipWarn = func(p, m string) { seen[p+":"+m]++ }

	req := &model.ChatCompletionRequest{Model: "smart", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	_, _, err = rtr.Route(context.Background(), req, RequestMeta{})
	require.NoError(t, err)
	_, _, err = rtr.Route(context.Background(), req, RequestMeta{})
	require.NoError(t, err)

	assert.Equal(t, 1, seen["anthropic:claude-missing"])
	assert.Equal(t, 1, seen["google:gemini-missing"])
}
