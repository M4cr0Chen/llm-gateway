package strategy

import (
	"context"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// stubProvider is a no-op provider whose only purpose in these tests is
// to give a Candidate a non-nil Provider with a known Name. We need
// distinct names so the strategies (latency, etc.) can key their state
// off them.
type stubProvider struct {
	name string
}

func (s stubProvider) Name() string    { return s.name }
func (s stubProvider) Models() []string { return nil }
func (s stubProvider) ChatCompletion(context.Context, *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return nil, nil
}
func (s stubProvider) ChatCompletionStream(context.Context, *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	return nil, nil
}

// stubCandidate is a convenience factory: pass the provider/model name
// plus a template Candidate carrying only the fields the test cares
// about (cost / priority / weight). Saves boilerplate vs. spelling out
// the full struct literal in every row of a table-driven test.
func stubCandidate(provName, modelName string, tmpl router.Candidate) router.Candidate {
	tmpl.Provider = stubProvider{name: provName}
	tmpl.Model = modelName
	return tmpl
}
