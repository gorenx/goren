package apiproxy

import (
	"context"
	"errors"
)

// HostDescribeMethod is the canonical method and HTTP endpoint segment.
const HostDescribeMethod = "host.describe"

// HostDescribeRequest is the empty host.describe request payload.
type HostDescribeRequest struct{}

// HostDescription is the one-shot Host capability snapshot consumed by the
// existing TypeScript client.
type HostDescription struct {
	Version          string `json:"version"`
	CWD              string `json:"cwd"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	AttachedSessions int    `json:"attachedSessions"`
	CanOpenPath      bool   `json:"canOpenPath"`
}

// HostDescriptionProvider supplies the live values owned by the assembled
// Host runtime; API Proxy only maps them to the wire method.
type HostDescriptionProvider interface {
	DescribeHost(context.Context) (HostDescription, error)
}

// HostDescriptionFunc adapts a function to HostDescriptionProvider.
type HostDescriptionFunc func(context.Context) (HostDescription, error)

// DescribeHost calls the adapted function.
func (operation HostDescriptionFunc) DescribeHost(requestContext context.Context) (HostDescription, error) {
	return operation(requestContext)
}

// RegisterHostDescribe installs host.describe in a Catalog.
func RegisterHostDescribe(methods *Catalog, source HostDescriptionProvider) error {
	if source == nil {
		return errors.New("apiproxy: host description provider is nil")
	}
	return RegisterUnary(methods, HostDescribeMethod, DecodeObject[HostDescribeRequest],
		func(requestContext context.Context, _ Request[HostDescribeRequest]) (Outcome[HostDescription], error) {
			snapshot, err := source.DescribeHost(requestContext)
			if err != nil {
				return Outcome[HostDescription]{}, err
			}
			if snapshot.AttachedSessions < 0 {
				return Outcome[HostDescription]{}, errors.New("apiproxy: host description has negative attachedSessions")
			}
			return OK(snapshot), nil
		})
}
