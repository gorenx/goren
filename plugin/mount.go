package plugin

import (
	"errors"
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

func newPluginTarget(pluginInstance Plugin) (pluginTarget, error) {
	metadata, err := normalizeManifest(pluginInstance)
	if err != nil {
		return pluginTarget{}, err
	}
	return pluginTarget{
		instance: pluginInstance,
		manifest: metadata,
	}, nil
}

// pluginMount is the stable topology position identified by a Handle. Its
// current Fiber may change after dependency refresh or replacement.
type pluginMount struct {
	handleID uint64
	parent   *pluginMount
	children []*pluginMount
	scope    *scope
	target   pluginTarget
	current  *fiber
	removed  bool
}

// mountTree owns Handle identity, lifecycle ancestry, and Scope placement.
// Runtime serializes every mutation before calling it.
type mountTree struct {
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

func (tree *mountTree) admit(
	target pluginTarget,
	parent *pluginMount,
	placement scopePlacement,
) (*pluginMount, error) {
	selectedScope, err := tree.selectScope(parent, placement)
	if err != nil {
		return nil, err
	}
	tree.nextHandle++
	mounted := &pluginMount{
		handleID: tree.nextHandle,
		parent:   parent,
		scope:    selectedScope,
		target:   target,
	}
	if parent != nil {
		parent.children = append(parent.children, mounted)
	}
	tree.ordered = append(tree.ordered, mounted)
	tree.byHandle[mounted.handleID] = mounted
	return mounted, nil
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
	mounted := tree.byHandle[handleID]
	return mounted, mounted != nil && !mounted.removed
}

func (tree *mountTree) from(startIndex int) []*pluginMount {
	if startIndex < 0 || startIndex > len(tree.ordered) {
		return nil
	}
	return append([]*pluginMount(nil), tree.ordered[startIndex:]...)
}

func (tree *mountTree) all() []*pluginMount {
	return tree.ordered
}

func (tree *mountTree) length() int {
	return len(tree.ordered)
}

func (tree *mountTree) markRemoved(mounted *pluginMount) {
	if mounted == nil || mounted.removed {
		return
	}
	mounted.removed = true
	for _, childMount := range mounted.children {
		tree.markRemoved(childMount)
	}
}

func (tree *mountTree) markAllRemoved() {
	for _, mounted := range tree.ordered {
		mounted.removed = true
	}
}

func (tree *mountTree) deleteTree(mounted *pluginMount) {
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
	tree.ordered = nil
	tree.byHandle = make(map[uint64]*pluginMount)
}
