package provider

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Registry maps model names to providers.
type Registry struct {
	mu     sync.RWMutex
	models map[string]Provider
}

// NewRegistry creates a new empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		models: make(map[string]Provider),
	}
}

// Register maps each model name to the given provider.
// If a model name is already registered, it is overwritten.
func (r *Registry) Register(p Provider, models []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, m := range models {
		r.models[m] = p
	}
}

// RegisterAlias maps an alias to the same provider as an existing target model.
// Returns an error if the target model is not registered or if the alias
// conflicts with an already-registered canonical model name.
func (r *Registry) RegisterAlias(alias, target string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.models[alias]; exists {
		return fmt.Errorf("alias %q conflicts with already-registered model", alias)
	}
	p, ok := r.models[target]
	if !ok {
		return fmt.Errorf("alias %q targets unregistered model %q", alias, target)
	}
	r.models[alias] = p
	return nil
}

// Resolve returns the provider registered for the given model name.
// Returns an error listing available models if the model is not found.
func (r *Registry) Resolve(modelName string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.models[modelName]
	if !ok {
		available := r.listModelsLocked()
		return nil, fmt.Errorf("unknown model %q: available models: [%s]", modelName, strings.Join(available, ", "))
	}
	return p, nil
}

// ListModels returns a sorted list of all registered model names.
func (r *Registry) ListModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.listModelsLocked()
}

func (r *Registry) listModelsLocked() []string {
	names := make([]string, 0, len(r.models))
	for name := range r.models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ModelInfo pairs a model name with its provider name.
type ModelInfo struct {
	ModelName    string
	ProviderName string
}

// ListModelDetails returns details for all registered models, sorted by model name.
func (r *Registry) ListModelDetails() []ModelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]ModelInfo, 0, len(r.models))
	for name, p := range r.models {
		infos = append(infos, ModelInfo{ModelName: name, ProviderName: p.Name()})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ModelName < infos[j].ModelName
	})
	return infos
}
