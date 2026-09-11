package server

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	mcpapi "github.com/gorenx/goren/apiproxy/mcp"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/tools/tool"
	"github.com/gorenx/goren/userquestions"
	goakt "github.com/tochemey/goakt/v4/actor"
)

// mcpManager owns process-local connections and their publication lifetime.
// API consumers use a Grain identity, never a pointer back to this owner.
type mcpManager struct {
	system    goakt.ActorSystem
	directory *goakt.GrainIdentity
	timeout   time.Duration
	questions userquestions.UserQuestions
	lifetime  context.Context
	cancel    context.CancelFunc
	mutations chan struct{}
	mu        sync.RWMutex
	entries   map[string]mcpEntry
	workers   sync.WaitGroup
}

type mcpEntry struct {
	connection *mcpclient.Connection
	transport  string
}

func newMCPManager(system goakt.ActorSystem, directory *goakt.GrainIdentity, timeout time.Duration, questions userquestions.UserQuestions) *mcpManager {
	lifetime, cancel := context.WithCancel(context.Background())
	return &mcpManager{
		system:    system,
		directory: directory,
		timeout:   timeout,
		questions: questions,
		lifetime:  lifetime,
		cancel:    cancel,
		mutations: make(chan struct{}, 1),
		entries:   make(map[string]mcpEntry),
	}
}

func (owner *mcpManager) acquire(ctx context.Context) error {
	select {
	case owner.mutations <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-owner.mutations
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (owner *mcpManager) connect(ctx context.Context, settings mcpclient.Config) (mcpapi.View, error) {
	bounded, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(owner.lifetime, cancel)
	defer stop()
	if err := owner.acquire(bounded); err != nil {
		return mcpapi.View{}, err
	}
	defer func() { <-owner.mutations }()
	if owner.lifetime.Err() != nil {
		return mcpapi.View{}, errors.New("server: MCP manager stopped")
	}
	owner.mu.RLock()
	_, exists := owner.entries[settings.ServerName]
	owner.mu.RUnlock()
	if exists {
		return mcpapi.View{}, errors.New("server: MCP name already connected")
	}
	connection, err := mcpclient.Open(bounded, settings, owner.questions)
	if err != nil {
		return mcpapi.View{}, err
	}
	entry := mcpEntry{
		connection: connection,
		transport:  settings.Transport,
	}
	owner.mu.Lock()
	owner.entries[settings.ServerName] = entry
	owner.mu.Unlock()
	if err := publishMCP(bounded, owner.system, owner.directory, owner.timeout, "mcp:"+settings.ServerName, connection.Schemas()); err != nil {
		// Keep ownership until cleanup succeeds; process shutdown can retry it.
		cleanup, release := context.WithTimeout(context.Background(), owner.timeout)
		defer release()
		withdrawal := publishMCP(cleanup, owner.system, owner.directory, owner.timeout, "mcp:"+settings.ServerName, nil)
		closed := connection.Close(cleanup)
		if withdrawal == nil && closed == nil {
			owner.mu.Lock()
			delete(owner.entries, settings.ServerName)
			owner.mu.Unlock()
		}
		return mcpapi.View{}, errors.Join(err, withdrawal, closed)
	}
	owner.workers.Add(1)
	go owner.watch(settings.ServerName, connection)
	return mcpView(settings.ServerName, entry), nil
}

// Provider resolves the current connection without waiting for network I/O.
func (owner *mcpManager) Provider(name string) (tool.Provider, bool) {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	if owner.lifetime.Err() != nil {
		return nil, false
	}
	var selected tool.Provider
	for _, entry := range owner.entries {
		candidate, exists := entry.connection.Provider(name)
		if exists {
			if selected != nil {
				return nil, false
			}
			selected = candidate
		}
	}
	return selected, selected != nil
}

func (owner *mcpManager) listConnections() []mcpapi.View {
	owner.mu.RLock()
	defer owner.mu.RUnlock()
	views := make([]mcpapi.View, 0, len(owner.entries))
	for name, entry := range owner.entries {
		views = append(views, mcpView(name, entry))
	}
	sort.Slice(views, func(left, right int) bool { return views[left].ServerName < views[right].ServerName })
	return views
}

func mcpView(name string, entry mcpEntry) mcpapi.View {
	status := "connected"
	count := len(entry.connection.Schemas())
	select {
	case <-entry.connection.Done():
		status = "failed"
		count = 0
	default:
	}
	return mcpapi.View{
		ServerName: name,
		Transport:  entry.transport,
		Status:     status,
		ToolCount:  count,
	}
}
