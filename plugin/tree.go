package plugin

import (
	"errors"
	"fmt"
)

// pluginDeclaration is one immutable node produced before Runtime admission.
// It is deliberately private: callers declare ownership through Manifest and
// never manipulate Runtime topology directly.
type pluginDeclaration struct {
	target    pluginTarget
	placement scopePlacement
	phase     ActivationPhase
	children  []*pluginDeclaration
}

type pluginForest struct {
	roots []*pluginDeclaration
}

func buildPluginForest(instances []Plugin) (pluginForest, error) {
	forest := pluginForest{
		roots: make([]*pluginDeclaration, 0, len(instances)),
	}
	seen := make(map[*Base]string)
	path := make(map[*Base]string)
	for instanceIndex, pluginInstance := range instances {
		rootPath := fmt.Sprintf("root[%d]", instanceIndex)
		root, err := buildPluginDeclaration(
			pluginInstance,
			mountAtRootScope,
			ActivationMain,
			rootPath,
			seen,
			path,
		)
		if err != nil {
			return pluginForest{}, err
		}
		forest.roots = append(forest.roots, root)
	}
	return forest, nil
}

func buildPluginDeclaration(
	pluginInstance Plugin,
	placement scopePlacement,
	phase ActivationPhase,
	pathName string,
	seen map[*Base]string,
	ancestors map[*Base]string,
) (*pluginDeclaration, error) {
	if pluginInstance == nil {
		return nil, fmt.Errorf("plugin: %s: cannot mount nil Plugin", pathName)
	}
	pluginBase, err := pluginBaseOf(pluginInstance)
	if err != nil {
		return nil, fmt.Errorf("plugin: %s: %w", pathName, err)
	}
	if pluginBase.currentFiber() != nil {
		return nil, fmt.Errorf(
			"plugin: %s: Plugin instance is already mounted",
			pathName,
		)
	}
	if ancestorPath, cyclic := ancestors[pluginBase]; cyclic {
		return nil, fmt.Errorf(
			"plugin: %s: Plugin ownership cycle returns to %s",
			pathName,
			ancestorPath,
		)
	}
	if previousPath, duplicate := seen[pluginBase]; duplicate {
		return nil, fmt.Errorf(
			"plugin: %s: Plugin instance is already declared at %s",
			pathName,
			previousPath,
		)
	}
	metadata, err := manifestOf(pluginInstance)
	if err != nil {
		return nil, fmt.Errorf("plugin: %s: %w", pathName, err)
	}
	target, err := newPluginTarget(pluginInstance, metadata)
	if err != nil {
		return nil, fmt.Errorf("plugin: %s: %w", pathName, err)
	}
	if err = validateActivationPhase(phase, target); err != nil {
		return nil, fmt.Errorf("plugin: %s: %w", pathName, err)
	}
	declared := &pluginDeclaration{
		target:    target,
		placement: placement,
		phase:     phase,
		children:  make([]*pluginDeclaration, 0, len(metadata.Children)),
	}
	seen[pluginBase] = pathName
	ancestors[pluginBase] = pathName
	for childIndex, child := range metadata.Children {
		childPath := fmt.Sprintf(
			"%s/%s.children[%d]",
			pathName,
			target.manifest.name,
			childIndex,
		)
		normalizedPlacement, placementErr := normalizeChildPlacement(child.Placement)
		if placementErr != nil {
			return nil, fmt.Errorf("plugin: %s: %w", childPath, placementErr)
		}
		if child.Phase != ActivationMain && child.Phase != ActivationCommit {
			return nil, fmt.Errorf("plugin: %s: unsupported activation phase", childPath)
		}
		if phase == ActivationCommit && child.Phase != ActivationCommit {
			return nil, fmt.Errorf(
				"plugin: %s: commit-phase Plugin cannot own a main-phase child",
				childPath,
			)
		}
		childNode, childErr := buildPluginDeclaration(
			child.Instance,
			normalizedPlacement,
			child.Phase,
			childPath,
			seen,
			ancestors,
		)
		if childErr != nil {
			return nil, childErr
		}
		declared.children = append(declared.children, childNode)
	}
	delete(ancestors, pluginBase)
	return declared, nil
}

func pluginBaseOf(pluginInstance Plugin) (pluginBase *Base, baseErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			pluginBase = nil
			baseErr = fmt.Errorf("Plugin.RuntimePlugin panicked: %v", recovered)
		}
	}()
	pluginBase = pluginInstance.RuntimePlugin()
	if pluginBase == nil {
		return nil, errors.New("Plugin returned a nil Base")
	}
	return pluginBase, nil
}

func manifestOf(pluginInstance Plugin) (metadata Manifest, manifestErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			metadata = Manifest{}
			manifestErr = fmt.Errorf("Plugin.Manifest panicked: %v", recovered)
		}
	}()
	metadata = pluginInstance.Manifest()
	metadata.Provides = append([]ProvidedService(nil), metadata.Provides...)
	metadata.Requires = append([]ServiceType(nil), metadata.Requires...)
	metadata.Optional = append([]ServiceType(nil), metadata.Optional...)
	metadata.Events = append([]EventSubscription(nil), metadata.Events...)
	metadata.Waterfalls = append(
		[]WaterfallMiddlewareBinding(nil),
		metadata.Waterfalls...,
	)
	metadata.Children = append([]ChildPlugin(nil), metadata.Children...)
	return metadata, nil
}

func validateActivationPhase(phase ActivationPhase, target pluginTarget) error {
	if phase != ActivationMain && phase != ActivationCommit {
		return errors.New("unsupported activation phase")
	}
	if phase == ActivationCommit && len(target.manifest.provides) != 0 {
		return errors.New("commit-phase Plugin cannot provide Services")
	}
	return nil
}

func normalizeChildPlacement(placement ChildPlacement) (scopePlacement, error) {
	switch placement {
	case SameScope:
		return mountInParentScope, nil
	case NestedScope:
		return mountInChildScope, nil
	default:
		return mountAtRootScope, errors.New("unsupported child Scope placement")
	}
}

func flattenDeclaration(root *pluginDeclaration) []*pluginDeclaration {
	if root == nil {
		return nil
	}
	nodes := []*pluginDeclaration{root}
	for _, child := range root.children {
		nodes = append(nodes, flattenDeclaration(child)...)
	}
	return nodes
}
