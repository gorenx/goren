package extension

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
)

// Provisioner installs the Registry's current Extension view into one child.
type Provisioner struct {
	registry *Registry
}

// NewProvisioner constructs one Agent Provisioner for a child Scope.
func NewProvisioner(owner *Registry) *Provisioner {
	return &Provisioner{
		registry: owner,
	}
}

// Provision installs live Extensions in registration order. Any failure rolls
// back every installation acquired by this call before returning.
func (owner *Provisioner) Provision(
	requestContext context.Context,
	scope agent.Scope,
) (agent.Provisioning, error) {
	if owner == nil || owner.registry == nil || scope == nil {
		return nil, errors.New("subagent: Extension Provisioner is unavailable")
	}
	owner.registry.mutex.Lock()
	registrations := append(
		[]*registration(nil),
		owner.registry.registrations...,
	)
	owner.registry.mutex.Unlock()
	if len(registrations) == 0 {
		return nil, nil
	}
	acquired := &provisioning{}
	for _, record := range registrations {
		record.mutex.Lock()
		removed := record.state == registrationRemoved
		record.mutex.Unlock()
		if removed {
			continue
		}
		installation, installErr := record.extension.Install(
			requestContext,
			scope,
		)
		if installErr != nil {
			return nil, errors.Join(
				installErr,
				acquired.Dispose(context.WithoutCancel(requestContext)),
			)
		}
		if installation == nil || nilInterface(installation) {
			return nil, errors.Join(
				errors.New("subagent: child Extension returned a nil installation"),
				acquired.Dispose(context.WithoutCancel(requestContext)),
			)
		}
		installed := &effect{
			owner:        acquired,
			registration: record,
			installation: installation,
		}
		record.mutex.Lock()
		record.installations = append(record.installations, installed)
		removed = record.state == registrationRemoved
		record.mutex.Unlock()
		acquired.mutex.Lock()
		acquired.effects = append(acquired.effects, installed)
		acquired.mutex.Unlock()
		if removed {
			acquired.invalidate()
			if disposeErr := installed.Dispose(
				context.WithoutCancel(requestContext),
			); disposeErr != nil {
				return nil, errors.Join(
					disposeErr,
					acquired.Dispose(context.WithoutCancel(requestContext)),
				)
			}
		}
	}
	return acquired, nil
}

var _ agent.Provisioner = (*Provisioner)(nil)
