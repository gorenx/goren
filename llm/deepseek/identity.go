package deepseek

import (
	"errors"
	"github.com/gorenx/goren/llm/deepseek/anonymoususerid"
	"sync"
)

// Identity lazily resolves the installation identity used by provider HTTP requests.
// Concurrent call workers share its one store; it retains no model-call state.
type Identity struct {
	hostEnvironment anonymoususerid.Environment

	mu      sync.Mutex
	storage *anonymoususerid.Store
}

func (provider *Identity) UserID() (string, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.storage == nil {
		storage, err := anonymoususerid.New(provider.hostEnvironment)
		if err != nil {
			return "", err
		}
		provider.storage = storage
	}
	return provider.storage.Value()
}

// NewIdentity retains the environment without reading the filesystem at startup.
func NewIdentity(environment anonymoususerid.Environment) (*Identity, error) {
	if environment == nil {
		return nil, errors.New("llm-deepseek: identity environment is required")
	}
	return &Identity{
		hostEnvironment: environment,
	}, nil
}
