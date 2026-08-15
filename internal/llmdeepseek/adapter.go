package llmdeepseek

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gorenx/goren/llm"
)

// DefaultHarnessVersion is used in the outbound identity when assembly does not
// supply the pinned Harness version explicitly.
const DefaultHarnessVersion = "0.1.0-rc.5"

// RequestSender is the outbound HTTP capability consumed by the DeepSeek adapter.
type RequestSender interface {
	Do(*http.Request) (*http.Response, error)
}

// AdapterOptions supplies operation-local configuration and identity resolvers.
type AdapterOptions struct {
	CurrentOptions func() (ConnectionOptions, error)
	ResolveAPIKey  func(context.Context, ConnectionOptions) (string, error)
	ResolveUserID  func() (string, error)
	RequestSender  RequestSender
	Version        string
	Now            func() time.Time
}

// Adapter is the direct DeepSeek chat-completions outbound adapter.
type Adapter struct {
	currentOptions func() (ConnectionOptions, error)
	resolveAPIKey  func(context.Context, ConnectionOptions) (string, error)
	resolveUserID  func() (string, error)
	requestSender  RequestSender
	version        string
	now            func() time.Time
}

// NewAdapter constructs a transport adapter from consumer-owned resolution seams.
func NewAdapter(settings AdapterOptions) (*Adapter, error) {
	if settings.CurrentOptions == nil {
		return nil, errors.New("llm-deepseek: current options resolver is nil")
	}
	if settings.ResolveAPIKey == nil {
		return nil, errors.New("llm-deepseek: API key resolver is nil")
	}
	if settings.ResolveUserID == nil {
		return nil, errors.New("llm-deepseek: anonymous user id resolver is nil")
	}
	transport := settings.RequestSender
	if transport == nil {
		transport = http.DefaultClient
	}
	version := settings.Version
	if version == "" {
		version = DefaultHarnessVersion
	}
	now := settings.Now
	if now == nil {
		now = time.Now
	}
	return &Adapter{
		currentOptions: settings.CurrentOptions,
		resolveAPIKey:  settings.ResolveAPIKey,
		resolveUserID:  settings.ResolveUserID,
		requestSender:  transport,
		version:        version,
		now:            now,
	}, nil
}

// Stream creates a lazy pull stream so configuration and credentials are
// resolved exactly once when the request actually starts.
func (backend *Adapter) Stream(requestContext context.Context, requestOptions llm.GenerateOptions) (llm.ChunkStream, error) {
	if requestContext == nil {
		requestContext = context.Background()
	}
	operationContext, cancelOperation := context.WithCancel(requestContext)
	return &deepSeekStream{
		backend:          backend,
		requestOptions:   requestOptions,
		operationContext: operationContext,
		cancelOperation:  cancelOperation,
	}, nil
}
