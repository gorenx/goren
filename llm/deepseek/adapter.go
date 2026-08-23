package deepseek

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

// ConnectionSource supplies the current immutable connection generation.
type ConnectionSource interface {
	CurrentConnection() (ConnectionOptions, error)
}

// APIKeyResolver resolves one credential reference for a new request.
type APIKeyResolver interface {
	ResolveAPIKey(context.Context, ConnectionOptions) (string, error)
}

// UserIDProvider supplies the model-hidden Harness installation identity.
type UserIDProvider interface {
	UserID() (string, error)
}

// Clock supplies current time for provider retry metadata.
type Clock interface {
	Now() time.Time
}

// AdapterDependencies contains the capabilities required by the DeepSeek adapter.
type AdapterDependencies struct {
	Connections ConnectionSource
	Credentials APIKeyResolver
	Identity    UserIDProvider
	Requests    RequestSender
	Clock       Clock
	Version     string
}

// Adapter is the direct DeepSeek chat-completions outbound adapter.
type Adapter struct {
	connections ConnectionSource
	credentials APIKeyResolver
	identity    UserIDProvider
	requests    RequestSender
	clock       Clock
	version     string
}

type systemClock struct{}

func (systemClock) Now() time.Time {
	return time.Now()
}

// NewAdapter constructs a transport adapter from consumer-owned capabilities.
func NewAdapter(dependencies AdapterDependencies) (*Adapter, error) {
	if dependencies.Connections == nil {
		return nil, errors.New("llm-deepseek: connection source is nil")
	}
	if dependencies.Credentials == nil {
		return nil, errors.New("llm-deepseek: API key resolver is nil")
	}
	if dependencies.Identity == nil {
		return nil, errors.New("llm-deepseek: user id provider is nil")
	}
	requests := dependencies.Requests
	if requests == nil {
		requests = http.DefaultClient
	}
	activeClock := dependencies.Clock
	if activeClock == nil {
		activeClock = systemClock{}
	}
	version := dependencies.Version
	if version == "" {
		version = DefaultHarnessVersion
	}
	return &Adapter{
		connections: dependencies.Connections,
		credentials: dependencies.Credentials,
		identity:    dependencies.Identity,
		requests:    requests,
		clock:       activeClock,
		version:     version,
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
