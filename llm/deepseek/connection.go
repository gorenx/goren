package deepseek

import (
	"context"
	"errors"
	"fmt"
	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/llm"
)

// Connection supplies immutable provider options and resolves their credential reference.
// It owns no adapter registration or actor lifecycle.
type Connection struct {
	connection         ConnectionOptions
	credentialProvider credentials.Provider
}

// NewConnection validates typed configuration using the launching environment.
func NewConnection(settings Config, environment LaunchEnvironment, secrets credentials.Provider) (*Connection, error) {
	if environment == nil || secrets == nil {
		return nil, errors.New("llm-deepseek: environment and credentials are required")
	}
	resolved, err := ResolveOptions(settings, environment)
	if err != nil {
		return nil, err
	}
	return &Connection{
		connection:         resolved.Snapshot(),
		credentialProvider: secrets,
	}, nil
}

// CurrentConnection returns a detached request-generation snapshot.
func (instance *Connection) CurrentConnection() (ConnectionOptions, error) {
	return instance.connection.Snapshot(), nil
}

// ResolveAPIKey resolves the configured credential for one new request.
func (instance *Connection) ResolveAPIKey(
	requestContext context.Context,
	connection ConnectionOptions,
) (string, error) {
	credentialRef := connection.APIKeyEnv
	resolved, err := instance.credentialProvider.Resolve(requestContext, credentialRef)
	if err == nil {
		return resolved.Value, nil
	}
	if !errors.Is(err, credentials.ErrNotFound) {
		return "", err
	}
	return "", llm.MustLlmError(
		fmt.Sprintf(
			"llm-deepseek: no API key for provider route %q; configure %s in Web Settings or export it in the launching environment",
			ProviderRoute,
			connection.APIKeyEnv,
		),
		"MISSING_CREDENTIAL",
	)
}
