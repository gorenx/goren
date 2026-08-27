package extension

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/subagent"
)

// Provisioner installs the Registry's current Extension view into one child.
type Provisioner struct {
	registry *Registry
	selected []string
}

// NewProvisioner constructs the common Extension Provisioner installed for
// every child Scope.
func NewProvisioner(owner *Registry) *Provisioner {
	return &Provisioner{
		registry: owner,
	}
}

// NewSelectedProvisioner validates and snapshots one caller-selected named
// Extension list. It never installs common Extensions.
func NewSelectedProvisioner(
	owner *Registry,
	extensionNames []string,
) (*Provisioner, error) {
	if owner == nil {
		return nil, errors.New("subagent: Extension Registry is unavailable")
	}
	if _, err := owner.selected(extensionNames); err != nil {
		return nil, err
	}
	configured := &Provisioner{
		registry: owner,
		selected: append([]string(nil), extensionNames...),
	}
	return configured, nil
}

// Provision installs live Extensions in registration order. Any failure rolls
// back every installation acquired by this call before returning.
func (owner *Provisioner) Provision(
	ctx context.Context,
	scope agent.Scope,
) (agent.Provisioning, error) {
	if owner == nil || owner.registry == nil || scope == nil {
		return nil, errors.New("subagent: Extension Provisioner is unavailable")
	}
	var registrations []*registration
	if owner.selected == nil {
		registrations = owner.registry.common()
	} else {
		var err error
		registrations, err = owner.registry.selected(owner.selected)
		if err != nil {
			return nil, err
		}
	}
	if len(registrations) == 0 {
		return nil, nil
	}
	acquired := &provisioning{}
	for _, record := range registrations {
		record.mutex.Lock()
		removed := record.state == registrationRemoved
		record.mutex.Unlock()
		if removed {
			if owner.selected != nil {
				return nil, errors.Join(
					&subagent.Error{
						Code: subagent.ErrorUnknownExtension,
						Message: "a selected child Extension is no longer " +
							"registered",
					},
					acquired.Dispose(context.WithoutCancel(ctx)),
				)
			}
			continue
		}
		installation, installErr := record.extension.Install(
			ctx,
			scope,
		)
		if installErr != nil {
			return nil, errors.Join(
				installErr,
				acquired.Dispose(context.WithoutCancel(ctx)),
			)
		}
		if installation == nil || nilInterface(installation) {
			return nil, errors.Join(
				errors.New("subagent: child Extension returned a nil installation"),
				acquired.Dispose(context.WithoutCancel(ctx)),
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
			if disposeErr := installed.Dispose(
				context.WithoutCancel(ctx),
			); disposeErr != nil {
				return nil, errors.Join(
					disposeErr,
					acquired.Dispose(context.WithoutCancel(ctx)),
				)
			}
		}
	}
	return acquired, nil
}

var _ agent.Provisioner = (*Provisioner)(nil)
