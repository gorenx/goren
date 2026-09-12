package runtimeactor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
	"github.com/gorenx/goren/userquestions"
)

// connections owns one Runtime's MCP resources. Runtime serializes lifecycle
// operations; ToolActors may concurrently borrow Providers through the read lock.
type connections struct {
	mu            sync.RWMutex
	configuration []mcpclient.Config
	questions     userquestions.UserQuestions
	servers       map[string]*mcpclient.Connection
	// resources retains current and replaced connections until release is confirmed.
	resources []*mcpclient.Connection
}

func (owner *connections) reconcile(ctx context.Context, definitions []mcpclient.Config) error {
	if len(definitions) != 0 && owner.questions == nil {
		return errors.New("tools: Runtime interaction capability is not installed")
	}
	selected := make([]mcpclient.Config, 0, len(definitions))
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		configuration, err := definition.Validate()
		if err != nil {
			return err
		}
		if _, found := names[configuration.ServerName]; found {
			return errors.New("tools: duplicate MCP connection name")
		}
		names[configuration.ServerName] = struct{}{}
		selected = append(selected, configuration)
	}
	previous := make(map[string]mcpclient.Config, len(owner.configuration))
	for _, configuration := range owner.configuration {
		previous[configuration.ServerName] = configuration
	}
	retained := make(map[tools.ProviderID]struct{}, len(owner.resources))
	for _, peer := range owner.resources {
		retained[peer.ProviderID()] = struct{}{}
	}
	next := make(map[string]*mcpclient.Connection, len(selected))
	for _, configuration := range selected {
		name := configuration.ServerName
		peer := owner.servers[name]
		if peer != nil && previous[name].Equal(configuration) {
			next[name] = peer
			continue
		}
		opened, err := mcpclient.Open(ctx, configuration, owner.questions)
		if err != nil {
			return errors.Join(err, owner.release(ctx, retained))
		}
		owner.mu.Lock()
		owner.resources = append(owner.resources, opened)
		owner.mu.Unlock()
		next[name] = opened
	}
	owner.mu.Lock()
	owner.servers = next
	owner.configuration = selected
	owner.mu.Unlock()
	return nil
}

// Provider resolves only this Runtime's exact connection identity.
func (owner *connections) Provider(name string, expected tools.ProviderID) (tool.Provider, bool) {
	if owner == nil || expected == "" {
		return nil, false
	}
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	for _, peer := range owner.resources {
		if peer.ProviderID() == expected && connectionAlive(peer) {
			return peer.Provider(name)
		}
	}
	return nil, false
}

func connectionAlive(peer *mcpclient.Connection) bool {
	select {
	case <-peer.Done():
		return false
	default:
		return true
	}
}

func (owner *connections) changed() bool {
	if owner == nil {
		return false
	}
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	changed := false
	for _, peer := range owner.servers {
		select {
		case <-peer.Changes():
			changed = true
		default:
		}
		if !connectionAlive(peer) {
			changed = true
		}
	}
	return changed
}

func (owner *connections) refresh(ctx context.Context) error {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	for _, peer := range owner.servers {
		if connectionAlive(peer) {
			if err := peer.Refresh(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// catalog adds this Runtime's discovered bindings to a detached configuration.
// Connections and ProviderIDs never enter the shared Registry's MCP definitions.
func (owner *connections) catalog(current toolregistry.Catalog) (toolregistry.Catalog, error) {
	selected := current.Clone()
	if selected.ProviderIDs == nil {
		selected.ProviderIDs = make(map[string]tools.ProviderID)
	}
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	names := make(map[string]struct{}, len(selected.Schemas))
	for _, schema := range selected.Schemas {
		if _, found := names[schema.Name]; found {
			return toolregistry.Catalog{}, errors.New("tools: duplicate catalog tool name")
		}
		names[schema.Name] = struct{}{}
	}
	for _, peer := range owner.servers {
		if !connectionAlive(peer) {
			continue
		}
		for _, schema := range peer.Schemas() {
			if _, found := names[schema.Name]; found {
				return toolregistry.Catalog{}, errors.New("tools: MCP tool name conflicts with another tool")
			}
			names[schema.Name] = struct{}{}
			selected.Schemas = append(selected.Schemas, schema)
			selected.ProviderIDs[schema.Name] = peer.ProviderID()
		}
	}
	slices.SortFunc(selected.Schemas, func(left, right llm.ToolSchema) int { return strings.Compare(left.Name, right.Name) })
	return selected, nil
}

func (owner *connections) retire(ctx context.Context, bindings map[string]tools.ProviderID) error {
	retained := make(map[tools.ProviderID]struct{}, len(bindings)+len(owner.servers))
	for _, identifier := range bindings {
		retained[identifier] = struct{}{}
	}
	for _, peer := range owner.servers {
		if connectionAlive(peer) {
			retained[peer.ProviderID()] = struct{}{}
		}
	}
	return owner.release(ctx, retained)
}

func (owner *connections) close(ctx context.Context) error {
	if owner == nil {
		return nil
	}
	return owner.release(ctx, nil)
}

func (owner *connections) release(ctx context.Context, retained map[tools.ProviderID]struct{}) error {
	// Lifecycle operations are serialized; Provider queries only read resources.
	var failures []error
	for index := len(owner.resources) - 1; index >= 0; index-- {
		peer := owner.resources[index]
		if _, needed := retained[peer.ProviderID()]; needed {
			continue
		}
		if err := peer.Close(ctx); err != nil {
			failures = append(failures, err)
			continue
		}
		owner.mu.Lock()
		owner.resources = slices.Delete(owner.resources, index, index+1)
		owner.mu.Unlock()
	}
	return errors.Join(failures...)
}
