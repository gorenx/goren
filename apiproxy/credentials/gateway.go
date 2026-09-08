package credentialsapi

import (
	"context"
	rpc "github.com/gorenx/goren/apiproxy/rpc"

	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/credentials"
)

// Provider is the narrow capability required by the Host API.
type Provider interface {
	Describe(context.Context, string) (credentials.Info, error)
	Set(context.Context, string, string) error
	Unset(context.Context, string) error
}

// Gateway maps the provider capability to value-free Host views.
type Gateway struct {
	provider Provider
}

// NewGateway constructs the credentials API owner.
func NewGateway(source Provider) *Gateway {
	return &Gateway{
		provider: source,
	}
}

// Describe returns configuration facts for the requested references.
func (owner *Gateway) Describe(
	requestContext context.Context,
	call rpc.Request[DescribeRequest],
) (rpc.Outcome[DescribeValue], error) {
	views := make(map[string]View, len(call.Payload.Refs))
	for _, ref := range call.Payload.Refs {
		if err := credentials.ValidateRef(ref); err != nil {
			return rpc.Outcome[DescribeValue]{}, err
		}
		info, err := owner.provider.Describe(requestContext, ref)
		if err != nil {
			return rpc.Outcome[DescribeValue]{}, err
		}
		views[ref] = View{
			Configured: info.Configured,
			Source:     info.Source,
			Writable:   info.Writable,
		}
	}
	return rpc.OK(DescribeValue{
		Credentials: views,
	}), nil
}

// Set commits one secret without echoing it in the result.
func (owner *Gateway) Set(
	requestContext context.Context,
	call rpc.Request[SetRequest],
) (rpc.Outcome[WriteValue], error) {
	ref := call.Payload.Ref
	if err := credentials.ValidateRef(ref); err != nil {
		return rpc.Outcome[WriteValue]{}, err
	}
	if err := owner.provider.Set(requestContext, ref, call.Payload.Value); err != nil {
		return rpc.Fail[WriteValue](credentialRejected(ref, err)), nil
	}
	return rpc.OK(WriteValue{}), nil
}

// Unset idempotently removes one managed secret.
func (owner *Gateway) Unset(
	requestContext context.Context,
	call rpc.Request[UnsetRequest],
) (rpc.Outcome[WriteValue], error) {
	ref := call.Payload.Ref
	if err := credentials.ValidateRef(ref); err != nil {
		return rpc.Outcome[WriteValue]{}, err
	}
	if err := owner.provider.Unset(requestContext, ref); err != nil {
		return rpc.Fail[WriteValue](credentialRejected(ref, err)), nil
	}
	return rpc.OK(WriteValue{}), nil
}

func credentialRejected(ref string, err error) connection.RPCError {
	return rpc.NewRPCError(connection.ErrorCredentialRejected, err.Error(), struct {
		Ref string `json:"ref"`
	}{
		Ref: ref,
	})
}
