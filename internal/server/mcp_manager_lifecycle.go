package server

import (
	"context"
	"log/slog"

	"github.com/gorenx/goren/mcpclient"
)

func (owner *mcpManager) watch(name string, connection *mcpclient.Connection) {
	defer owner.workers.Done()
	for {
		select {
		case <-owner.lifetime.Done():
			return
		case <-connection.Done():
			if err := owner.acquire(owner.lifetime); err != nil {
				return
			}
			owner.mu.RLock()
			current, exists := owner.entries[name]
			owner.mu.RUnlock()
			if exists && current.connection == connection {
				if err := publishMCP(owner.lifetime, owner.system, owner.directory, owner.timeout, "mcp:"+name, nil); err != nil {
					slog.Error("MCP tool withdrawal failed")
				}
				if err := connection.Close(owner.lifetime); err != nil {
					slog.Warn("MCP connection cleanup incomplete")
				}
			}
			<-owner.mutations
			return
		case <-connection.Changes():
			if err := owner.acquire(owner.lifetime); err != nil {
				return
			}
			owner.mu.RLock()
			current, exists := owner.entries[name]
			owner.mu.RUnlock()
			if exists && current.connection == connection {
				if err := connection.Refresh(owner.lifetime); err != nil {
					slog.Warn("MCP tools refresh failed")
				} else if err := publishMCP(owner.lifetime, owner.system, owner.directory, owner.timeout, "mcp:"+name, connection.Schemas()); err != nil {
					slog.Error("MCP tool publication failed")
				}
			}
			<-owner.mutations
		}
	}
}

func (owner *mcpManager) disconnect(ctx context.Context, name string) error {
	if err := owner.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-owner.mutations }()
	owner.mu.RLock()
	entry, exists := owner.entries[name]
	owner.mu.RUnlock()
	if !exists {
		return nil
	}
	if err := publishMCP(ctx, owner.system, owner.directory, owner.timeout, "mcp:"+name, nil); err != nil {
		return err
	}
	if err := entry.connection.Close(ctx); err != nil {
		return err
	}
	owner.mu.Lock()
	delete(owner.entries, name)
	owner.mu.Unlock()
	return nil
}

func (owner *mcpManager) close(ctx context.Context) error {
	owner.cancel()
	if err := owner.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-owner.mutations }()
	// No new connection can start after the lifetime is canceled and the gate
	// has drained. Workers exit without acquiring the gate on cancellation.
	completed := make(chan struct{})
	go func() {
		owner.workers.Wait()
		close(completed)
	}()
	select {
	case <-completed:
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, entry := range owner.entries {
		if err := entry.connection.Close(ctx); err != nil {
			return err
		}
	}
	return nil
}
