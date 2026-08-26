package report

import (
	"context"
	"errors"
	"sync"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/agent/scopedplugin"
	"github.com/gorenx/goren/subagent"
)

type extension struct{}

func (contribution *extension) Install(
	requestContext context.Context,
	scope agent.Scope,
) (subagent.ExtensionInstallation, error) {
	if scope == nil {
		return nil, errors.New("subagent report: child Scope is unavailable")
	}
	installed := &installation{}
	child := &childPlugin{
		installation: installed,
	}
	mounted, mountErr := scopedplugin.Mount(
		requestContext,
		scope,
		child,
	)
	if mountErr != nil {
		return nil, mountErr
	}
	installed.attach(mounted)
	return installed, nil
}

// installation owns the exact mounted child Plugin. The child Plugin marks
// the installation released during ordinary Agent Scope teardown so a later
// provisioning cleanup does not attempt a nested Runtime topology change.
type installation struct {
	mutex        sync.Mutex
	once         sync.Once
	mounted      agent.ScopeResource
	released     bool
	releaseErr   error
	uninstallErr error
}

func (installed *installation) attach(mounted agent.ScopeResource) {
	installed.mutex.Lock()
	installed.mounted = mounted
	installed.mutex.Unlock()
}

func (installed *installation) release(releaseErr error) {
	installed.mutex.Lock()
	installed.released = true
	installed.releaseErr = releaseErr
	installed.mutex.Unlock()
}

func (installed *installation) Uninstall(closeContext context.Context) error {
	if installed == nil {
		return nil
	}
	installed.once.Do(func() {
		installed.mutex.Lock()
		if installed.released {
			installed.uninstallErr = installed.releaseErr
			installed.mutex.Unlock()
			return
		}
		mounted := installed.mounted
		installed.mutex.Unlock()
		if mounted == nil {
			installed.mutex.Lock()
			installed.uninstallErr = errors.New(
				"subagent report: child Plugin installation is unavailable",
			)
			installed.mutex.Unlock()
			return
		}
		if closeContext == nil {
			closeContext = context.Background()
		}
		unloadErr := mounted.Dispose(context.WithoutCancel(closeContext))
		installed.mutex.Lock()
		if unloadErr != nil {
			installed.uninstallErr = unloadErr
		} else {
			installed.uninstallErr = installed.releaseErr
		}
		installed.mutex.Unlock()
	})
	installed.mutex.Lock()
	uninstallErr := installed.uninstallErr
	installed.mutex.Unlock()
	return uninstallErr
}

var _ subagent.Extension = (*extension)(nil)
var _ subagent.ExtensionInstallation = (*installation)(nil)
