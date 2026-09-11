package mcpclient

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/userquestions"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Connection owns one SDK transport, subscription and in-flight calls. It is safe
// for concurrent tool calls; Refresh is serialized and publishes whole snapshots.
type Connection struct {
	settings      Config
	session       *protocolSession
	lifetime      context.Context
	cancel        context.CancelFunc
	mutex         sync.Mutex
	refresh       sync.Mutex
	entries       map[string]remoteTool
	failure       error
	closed        bool
	calls         sync.WaitGroup
	notifications *subscription
	done          chan struct{}
	closeOnce     sync.Once
	released      chan struct{}
	closeErr      error
}

// Open starts and validates a complete connection before returning it. The caller
// owns Close even when its own startup Context later expires.
func Open(ctx context.Context, settings Config, questions userquestions.UserQuestions) (*Connection, error) {
	if ctx == nil {
		return nil, errors.New("mcpclient: startup context is required")
	}
	validated, err := settings.Validate()
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	owner := &Connection{
		settings:      validated,
		lifetime:      lifetime,
		cancel:        cancel,
		notifications: newSubscription(),
		done:          make(chan struct{}),
		released:      make(chan struct{}),
	}
	var wire transport.Interface
	if validated.Transport == "stdio" {
		wire = transport.NewStdioWithOptions(validated.Command, os.Environ(), validated.Args, transport.WithCommandFunc(func(_ context.Context, command string, environment []string, arguments []string) (*exec.Cmd, error) {
			process := exec.Command(command, arguments...)
			process.Dir = validated.CWD
			process.Env = environment
			return process, nil
		}), transport.WithCommandLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	} else {
		wire, err = transport.NewStreamableHTTP(validated.URL,
			transport.WithHTTPBasicClient(&http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			}),
			transport.WithHTTPLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	}
	if err != nil {
		cancel()
		return nil, errors.New("mcpclient: cannot construct transport")
	}
	owner.session, err = newProtocolSession(wire, protocolSettings{
		skipDiscover: validated.SkipDiscover,
		identity: mcp.Implementation{
			Name:    "goren-mcp-client",
			Version: "1.0.0",
		},
		elicitation: &elicitor{
			serverName: validated.ServerName,
			questions:  questions,
		},
		requestTimeout:     time.Duration(validated.RequestTimeoutMs) * time.Millisecond,
		toolResultLimit:    validated.ToolResultLimit,
		maxInputRoundTrips: validated.MaxInputRoundTrips,
	})
	if err != nil {
		cancel()
		_ = wire.Close()
		return nil, err
	}
	owner.session.onNotification(owner.notifications.receive)
	// Transport lifetime must outlive the caller's startup deadline, while the
	// network operations during startup must still respect caller cancellation.
	stop := context.AfterFunc(ctx, cancel)
	err = owner.session.start(lifetime)
	if err != nil {
		stop()
		cancel()
		_ = owner.session.close()
		return nil, err
	}
	go owner.listen()
	startup, release := context.WithTimeout(ctx, time.Duration(validated.RequestTimeoutMs)*time.Millisecond)
	defer release()
	select {
	case err = <-owner.notifications.ack:
	case <-owner.done:
		err = owner.Cause()
	case <-startup.Done():
		err = startup.Err()
	}
	if err == nil {
		err = owner.Refresh(startup)
	}
	stop()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = owner.Close(context.Background())
		return nil, err
	}
	return owner, nil
}

func (owner *Connection) listen() {
	err := owner.session.listen(owner.lifetime, mcp.SubscriptionFilter{
		ToolsListChanged: true,
	})
	owner.mutex.Lock()
	if !owner.closed {
		owner.failure = errors.New("mcpclient: subscription stream ended")
		if err != nil {
			owner.failure = errors.New("mcpclient: subscription connection failed")
		}
	}
	owner.mutex.Unlock()
	owner.cancel()
	close(owner.done)
}

// Changes reports coalesced notifications. Refresh must consume the full list.
func (owner *Connection) Changes() <-chan struct{} { return owner.notifications.changes }

// Done closes whenever the subscription ends, including a successful remote result.
func (owner *Connection) Done() <-chan struct{} { return owner.done }

// Cause reports connection failure without exposing remote URLs or credentials.
func (owner *Connection) Cause() error {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return owner.failure
}

// Schemas returns detached model-facing configuration for the last complete list.
func (owner *Connection) Schemas() []llm.ToolSchema {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	return schemas(owner.entries)
}

// Close freezes admission and closes transport I/O, then drains all owned work.
// A timed-out caller can retry waiting for the same cleanup operation.
func (owner *Connection) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("mcpclient: close context is required")
	}
	owner.closeOnce.Do(func() {
		owner.mutex.Lock()
		owner.closed = true
		owner.mutex.Unlock()
		owner.cancel()
		go func() {
			owner.closeErr = owner.session.close()
			<-owner.done
			owner.calls.Wait()
			close(owner.released)
		}()
	})
	select {
	case <-owner.released:
		return owner.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (owner *Connection) begin(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("mcpclient: operation context is required")
	}
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	if owner.closed || owner.lifetime.Err() != nil {
		return nil, nil, errors.New("mcpclient: connection is unavailable")
	}
	work, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(owner.lifetime, cancel)
	owner.calls.Add(1)
	return work, func() { stop(); cancel(); owner.calls.Done() }, nil
}
