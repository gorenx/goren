package apiproxy

import (
	"context"

	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/credentials"
)

// CredentialProvider is the narrow capability required by the Host API.
type CredentialProvider interface {
	Describe(context.Context, credentials.Ref) (credentials.Info, error)
	Set(context.Context, credentials.Ref, string) error
	Unset(context.Context, credentials.Ref) error
}

// CredentialsGateway maps the provider capability to value-free Host views.
type CredentialsGateway struct {
	provider CredentialProvider
}

// NewCredentialsGateway constructs the credentials API owner.
func NewCredentialsGateway(provider CredentialProvider) *CredentialsGateway {
	return &CredentialsGateway{provider: provider}
}

// Describe returns configuration facts for the requested references.
func (gateway *CredentialsGateway) Describe(
	requestContext context.Context,
	call Request[CredentialsDescribeRequest],
) (Outcome[CredentialsDescribeValue], error) {
	views := make(map[string]CredentialView, len(call.Payload.Refs))
	for _, rawRef := range call.Payload.Refs {
		ref, err := credentials.NewRef(rawRef)
		if err != nil {
			return Outcome[CredentialsDescribeValue]{}, err
		}
		info, err := gateway.provider.Describe(requestContext, ref)
		if err != nil {
			return Outcome[CredentialsDescribeValue]{}, err
		}
		views[rawRef] = CredentialView{
			Configured: info.Configured, Source: info.Source, Writable: info.Writable,
		}
	}
	return OK(CredentialsDescribeValue{Credentials: views}), nil
}

// Set commits one secret without echoing it in the result.
func (gateway *CredentialsGateway) Set(
	requestContext context.Context,
	call Request[CredentialsSetRequest],
) (Outcome[CredentialsWriteValue], error) {
	ref, err := credentials.NewRef(call.Payload.Ref)
	if err != nil {
		return Outcome[CredentialsWriteValue]{}, err
	}
	if err := gateway.provider.Set(requestContext, ref, call.Payload.Value); err != nil {
		return Fail[CredentialsWriteValue](credentialRejected(ref, err)), nil
	}
	return OK(CredentialsWriteValue{}), nil
}

// Unset idempotently removes one managed secret.
func (gateway *CredentialsGateway) Unset(
	requestContext context.Context,
	call Request[CredentialsUnsetRequest],
) (Outcome[CredentialsWriteValue], error) {
	ref, err := credentials.NewRef(call.Payload.Ref)
	if err != nil {
		return Outcome[CredentialsWriteValue]{}, err
	}
	if err := gateway.provider.Unset(requestContext, ref); err != nil {
		return Fail[CredentialsWriteValue](credentialRejected(ref, err)), nil
	}
	return OK(CredentialsWriteValue{}), nil
}

func credentialRejected(ref credentials.Ref, err error) connection.RPCError {
	return NewRPCError(connection.ErrorCredentialRejected, err.Error(), struct {
		Ref string `json:"ref"`
	}{Ref: string(ref)})
}
