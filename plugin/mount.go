package plugin

import (
	"errors"
	"sync"
)

type scopePlacement uint8

const (
	mountAtRootScope scopePlacement = iota
	mountInParentScope
	mountInChildScope
)

// pluginTarget is the validated implementation currently assigned to one
// stable mount position.
type pluginTarget struct {
	instance Plugin
	manifest manifestSpec
}

func newPluginTarget(
	pluginInstance Plugin,
	metadata Manifest,
) (pluginTarget, error) {
	normalized, err := normalizeManifest(pluginInstance, metadata)
	if err != nil {
		return pluginTarget{}, err
	}
	return pluginTarget{
		instance: pluginInstance,
		manifest: normalized,
	}, nil
}

// pluginMount is the stable topology position identified by a Handle. Its
// current Fiber may change after dependency refresh or replacement.
type pluginMount struct {
	handleID  uint64
	parent    *pluginMount
	children  []*pluginMount
	scope     *scope
	placement scopePlacement
	phase     ActivationPhase
	target    pluginTarget
	current   *fiber
	removed   bool
}

// mountTree owns Handle identity, lifecycle ancestry, and Scope placement.
// Runtime serializes every mutation before calling it.
type mountTree struct {
	mutex      sync.RWMutex
	rootScope  *scope
	ordered    []*pluginMount
	byHandle   map[uint64]*pluginMount
	nextHandle uint64
}

func newMountTree() *mountTree {
	return &mountTree{
		rootScope: newRootScope(),
		byHandle:  make(map[uint64]*pluginMount),
	}
}

func (tree *mountTree) admitLocked(
	target pluginTarget,
	parent *pluginMount,
	placement scopePlacement,
	phase ActivationPhase,
) (*pluginMount, error) {
	selectedScope, err := tree.selectScope(parent, placement)
	if err != nil {
		return nil, err
	}
	tree.nextHandle++
	mounted := &pluginMount{
		handleID:  tree.nextHandle,
		parent:    parent,
		scope:     selectedScope,
		placement: placement,
		phase:     phase,
		target:    target,
	}
	if parent != nil {
		parent.children = append(parent.children, mounted)
	}
	tree.ordered = append(tree.ordered, mounted)
	tree.byHandle[mounted.handleID] = mounted
	return mounted, nil
}

func (tree *mountTree) admitDeclarations(
	declarations []*pluginDeclaration,
	parent *pluginMount,
	rootPlacement scopePlacement,
) ([]*pluginMount, []*pluginMount, error) {
	tree.mutex.Lock()
	defer tree.mutex.Unlock()
	roots := make([]*pluginMount, 0, len(declarations))
	admitted := make([]*pluginMount, 0)
	for _, declaration := range declarations {
		mounted, declarationMounts, err := tree.admitDeclarationLocked(
			declaration,
			parent,
			rootPlacement,
		)
		if err != nil {
			for _, rootMount := range roots {
				tree.deleteTreeLocked(rootMount)
			}
			return nil, nil, err
		}
		roots = append(roots, mounted)
		admitted = append(admitted, declarationMounts...)
	}
	return roots, admitted, nil
}

func (tree *mountTree) admitDeclarationLocked(
	declaration *pluginDeclaration,
	parent *pluginMount,
	rootPlacement scopePlacement,
) (*pluginMount, []*pluginMount, error) {
	if declaration == nil {
		return nil, nil, errors.New("plugin: cannot admit an empty Plugin tree")
	}
	mounted, err := tree.admitLocked(
		declaration.target,
		parent,
		rootPlacement,
		declaration.phase,
	)
	if err != nil {
		return nil, nil, err
	}
	admitted := []*pluginMount{mounted}
	for _, childDeclaration := range declaration.children {
		_, childMounts, childErr := tree.admitDeclarationLocked(
			childDeclaration,
			mounted,
			childDeclaration.placement,
		)
		if childErr != nil {
			tree.deleteTreeLocked(mounted)
			return nil, nil, childErr
		}
		admitted = append(admitted, childMounts...)
	}
	return mounted, admitted, nil
}

func (tree *mountTree) selectScope(
	parent *pluginMount,
	placement scopePlacement,
) (*scope, error) {
	switch placement {
	case mountAtRootScope:
		if parent != nil {
			return nil, errors.New("plugin: root Scope mount cannot have a parent")
		}
		return tree.rootScope, nil
	case mountInParentScope:
		if parent == nil {
			return nil, errors.New("plugin: inherited Scope mount requires a parent")
		}
		return parent.scope, nil
	case mountInChildScope:
		if parent == nil {
			return nil, errors.New("plugin: child Scope mount requires a parent")
		}
		return newChildScope(parent.scope), nil
	default:
		return nil, errors.New("plugin: unsupported Scope placement")
	}
}

func (tree *mountTree) lookup(handleID uint64) (*pluginMount, bool) {
	tree.mutex.RLock()
	defer tree.mutex.RUnlock()
	mounted := tree.byHandle[handleID]
	return mounted, mounted != nil && !mounted.removed
}

func (tree *mountTree) all() []*pluginMount {
	tree.mutex.RLock()
	defer tree.mutex.RUnlock()
	return append([]*pluginMount(nil), tree.ordered...)
}

func (tree *mountTree) length() int {
	tree.mutex.RLock()
	defer tree.mutex.RUnlock()
	return len(tree.ordered)
}

func (tree *mountTree) markRemoved(mounted *pluginMount) {
	tree.mutex.Lock()
	defer tree.mutex.Unlock()
	tree.markRemovedLocked(mounted)
}

func (tree *mountTree) markRemovedLocked(mounted *pluginMount) {
	if mounted == nil || mounted.removed {
		return
	}
	mounted.removed = true
	for _, childMount := range mounted.children {
		tree.markRemovedLocked(childMount)
	}
}

func (tree *mountTree) markAllRemoved() {
	tree.mutex.Lock()
	defer tree.mutex.Unlock()
	for _, mounted := range tree.ordered {
		mounted.removed = true
	}
}

func (tree *mountTree) deleteTree(mounted *pluginMount) {
	tree.mutex.Lock()
	defer tree.mutex.Unlock()
	tree.deleteTreeLocked(mounted)
}

func (tree *mountTree) deleteRoots(roots []*pluginMount) {
	tree.mutex.Lock()
	defer tree.mutex.Unlock()
	for _, mounted := range roots {
		tree.deleteTreeLocked(mounted)
	}
}

func (tree *mountTree) deleteTreeLocked(mounted *pluginMount) {
	if mounted == nil {
		return
	}
	removeSet := make(map[*pluginMount]struct{})
	var collect func(*pluginMount)
	collect = func(selectedMount *pluginMount) {
		if selectedMount == nil {
			return
		}
		removeSet[selectedMount] = struct{}{}
		for _, childMount := range selectedMount.children {
			collect(childMount)
		}
	}
	collect(mounted)
	for selectedMount := range removeSet {
		delete(tree.byHandle, selectedMount.handleID)
	}
	retained := tree.ordered[:0]
	for _, candidateMount := range tree.ordered {
		if _, removed := removeSet[candidateMount]; !removed {
			retained = append(retained, candidateMount)
		}
	}
	tree.ordered = retained
	if mounted.parent == nil {
		return
	}
	children := mounted.parent.children[:0]
	for _, childMount := range mounted.parent.children {
		if _, removed := removeSet[childMount]; !removed {
			children = append(children, childMount)
		}
	}
	mounted.parent.children = children
}

func (tree *mountTree) clear() {
	tree.mutex.Lock()
	defer tree.mutex.Unlock()
	tree.ordered = nil
	tree.byHandle = make(map[uint64]*pluginMount)
}

func (tree *mountTree) activeMounts() []*pluginMount {
	tree.mutex.RLock()
	defer tree.mutex.RUnlock()
	snapshots := make([]*pluginMount, 0, len(tree.ordered))
	for _, mounted := range tree.ordered {
		if !mounted.removed {
			snapshots = append(snapshots, mounted)
		}
	}
	return snapshots
}
