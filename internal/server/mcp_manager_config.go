package server

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"

	"github.com/gorenx/goren/mcpclient"
	usersettings "github.com/gorenx/goren/settings"
)

var _ usersettings.MCPConnections = (*mcpManager)(nil)

// Reconcile applies the complete HTTP target while preserving local stdio.
// The mutation gate serializes connection ownership, publication and cleanup;
// Provider lookups and existing tool calls do not wait on this gate.
func (owner *mcpManager) Reconcile(ctx context.Context, definitions []mcpclient.Config) error {
	if ctx == nil {
		return errors.New("server: MCP context is required")
	}
	bounded, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(owner.lifetime, cancel)
	defer stop()
	if err := owner.acquire(bounded); err != nil {
		return err
	}
	defer func() { <-owner.mutations }()
	if owner.lifetime.Err() != nil {
		return errors.New("server: MCP manager stopped")
	}
	target, err := owner.validateHTTP(definitions)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(target))
	for name := range target {
		names = append(names, name)
	}
	if err := owner.closeRetained(bounded); err != nil {
		return err
	}
	sort.Strings(names)
	var failures []error
	for _, name := range names {
		if err := bounded.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		owner.mu.RLock()
		current, exists := owner.entries[name]
		owner.mu.RUnlock()
		var err error
		if exists &&
			connectionAvailable(current.connection) &&
			sameMCPConfig(current.config, target[name]) {
			// A prior publication reply may have been lost. Republish the same
			// binding without opening a connection or changing schema versions.
			err = publishMCP(bounded, owner.system, owner.directory, owner.timeout, "mcp:"+name, current.connection)
		} else {
			err = owner.replaceConnection(bounded, target[name])
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	owner.mu.RLock()
	var removed []string
	for name, current := range owner.entries {
		_, wanted := target[name]
		if !wanted && current.config.Transport == "streamable-http" {
			removed = append(removed, name)
		}
	}
	owner.mu.RUnlock()
	sort.Strings(removed)
	for _, name := range removed {
		if err := bounded.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := owner.removeConnection(bounded, name); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// replaceConnection requires the mutation gate and a validated configuration.
func (owner *mcpManager) replaceConnection(ctx context.Context, input mcpclient.Config) error {
	owner.mu.RLock()
	previous, exists := owner.entries[input.ServerName]
	owner.mu.RUnlock()
	candidate, err := mcpclient.Open(ctx, input, owner.questions)
	if err != nil {
		return err
	}
	if exists && connectionAvailable(previous.connection) {
		if err := previous.connection.Drain(ctx); err != nil {
			resumed := previous.connection.Resume()
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), owner.timeout)
			defer cancel()
			return errors.Join(err, resumed, owner.releaseConnection(cleanup, candidate))
		}
	}
	owner.mu.Lock()
	owner.entries[input.ServerName] = mcpEntry{
		connection: candidate,
		config:     input,
	}
	owner.mu.Unlock()
	if err := publishMCP(ctx, owner.system, owner.directory, owner.timeout, "mcp:"+input.ServerName, candidate); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), owner.timeout)
		defer cancel()
		if exists {
			owner.mu.Lock()
			owner.entries[input.ServerName] = previous
			owner.mu.Unlock()
			publication := previous.connection
			if !connectionAvailable(publication) {
				publication = nil
			}
			restored := publishMCP(cleanup, owner.system, owner.directory, owner.timeout, "mcp:"+input.ServerName, publication)
			var resumed error
			if publication != nil {
				resumed = publication.Resume()
			}
			return errors.Join(err, restored, resumed, owner.releaseConnection(cleanup, candidate))
		}
		// Keep the failed entry if either withdrawal or release is unconfirmed.
		// A later target application or explicit disconnect still owns its name.
		withdrawn := publishMCP(cleanup, owner.system, owner.directory, owner.timeout, "mcp:"+input.ServerName, nil)
		closed := candidate.Close(cleanup)
		if withdrawn == nil && closed == nil {
			owner.mu.Lock()
			delete(owner.entries, input.ServerName)
			owner.mu.Unlock()
		}
		return errors.Join(err, withdrawn, closed)
	}
	owner.workers.Add(1)
	go owner.watch(input.ServerName, candidate)
	if exists {
		return owner.releaseConnection(ctx, previous.connection)
	}
	return nil
}

// removeConnection drains existing work; explicit disconnect keeps its immediate
// cancellation behavior in mcp_manager_lifecycle.go.
func (owner *mcpManager) removeConnection(ctx context.Context, name string) error {
	owner.mu.RLock()
	current := owner.entries[name]
	owner.mu.RUnlock()
	if connectionAvailable(current.connection) {
		if err := current.connection.Drain(ctx); err != nil {
			return errors.Join(err, current.connection.Resume())
		}
	}
	if err := publishMCP(ctx, owner.system, owner.directory, owner.timeout, "mcp:"+name, nil); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), owner.timeout)
		defer cancel()
		var restored, resumed error
		if connectionAvailable(current.connection) {
			restored = publishMCP(cleanup, owner.system, owner.directory, owner.timeout, "mcp:"+name, current.connection)
			resumed = current.connection.Resume()
		}
		return errors.Join(err, restored, resumed)
	}
	if err := current.connection.Close(ctx); err != nil {
		return err
	}
	owner.mu.Lock()
	delete(owner.entries, name)
	owner.mu.Unlock()
	return nil
}

// releaseConnection retains the exact replaced connection until Close succeeds.
// It is called only while the manager's mutation gate is held.
func (owner *mcpManager) releaseConnection(ctx context.Context, remote *mcpclient.Connection) error {
	owner.retained[remote.ProviderID()] = remote
	if err := remote.Close(ctx); err != nil {
		return err
	}
	delete(owner.retained, remote.ProviderID())
	return nil
}

func (owner *mcpManager) closeRetained(ctx context.Context) error {
	var failures []error
	for identifier, remote := range owner.retained {
		if err := remote.Close(ctx); err != nil {
			failures = append(failures, err)
		} else {
			delete(owner.retained, identifier)
		}
	}
	return errors.Join(failures...)
}

func connectionAvailable(remote *mcpclient.Connection) bool {
	select {
	case <-remote.Done():
		return false
	default:
		return true
	}
}

func sameMCPConfig(left, right mcpclient.Config) bool {
	if !slices.Equal(left.Args, right.Args) {
		return false
	}
	left.Args = nil
	right.Args = nil
	return reflect.DeepEqual(left, right)
}

// Validate checks the HTTP target against owner-defined transport and local-name
// constraints without connecting, publishing or waiting for active tool calls.
func (owner *mcpManager) Validate(ctx context.Context, definitions []mcpclient.Config) error {
	if ctx == nil {
		return errors.New("server: MCP context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner.lifetime.Err() != nil {
		return errors.New("server: MCP manager stopped")
	}
	_, err := owner.validateHTTP(definitions)
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (owner *mcpManager) validateHTTP(definitions []mcpclient.Config) (map[string]mcpclient.Config, error) {
	target := make(map[string]mcpclient.Config, len(definitions))
	for _, input := range definitions {
		validated, err := input.Validate()
		if err != nil {
			return nil, err
		}
		if validated.Transport != "streamable-http" {
			return nil, errors.New("server: Settings only configures HTTP MCP connections")
		}
		if _, exists := target[validated.ServerName]; exists {
			return nil, errors.New("server: duplicate MCP target name")
		}
		owner.mu.RLock()
		current, exists := owner.entries[validated.ServerName]
		owner.mu.RUnlock()
		if exists && current.config.Transport != "streamable-http" {
			return nil, errors.New("server: MCP target conflicts with local stdio")
		}
		target[validated.ServerName] = validated
	}
	return target, nil
}
