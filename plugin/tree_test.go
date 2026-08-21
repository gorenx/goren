package plugin_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/plugin"
)

type treePlugin struct {
	plugin.Base
	name          string
	children      []plugin.ChildPlugin
	order         *[]string
	applyErr      error
	manifestCalls int
	applyCalls    int
	disposeCalls  int
}

func (node *treePlugin) Manifest() plugin.Manifest {
	node.manifestCalls++
	return plugin.Manifest{
		Name:     node.name,
		Children: append([]plugin.ChildPlugin(nil), node.children...),
	}
}

func (node *treePlugin) Apply(context.Context) error {
	node.applyCalls++
	if node.order != nil {
		*node.order = append(*node.order, node.name+":apply")
	}
	return node.applyErr
}

func (node *treePlugin) Dispose(context.Context) error {
	node.disposeCalls++
	if node.order != nil {
		*node.order = append(*node.order, node.name+":dispose")
	}
	return nil
}

func TestRuntimeBuildsAndActivatesCompleteDeclaredTree(t *testing.T) {
	order := make([]string, 0)
	mainChild := &treePlugin{
		name:  "main-child",
		order: &order,
	}
	commitChild := &treePlugin{
		name:  "commit-child",
		order: &order,
	}
	root := &treePlugin{
		name:  "root",
		order: &order,
		children: []plugin.ChildPlugin{
			{
				Instance:  mainChild,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
			{
				Instance:  commitChild,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationCommit,
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), root); err != nil {
		t.Fatalf("start tree: %v", err)
	}
	if got := strings.Join(order, ","); got != "root:apply,main-child:apply,commit-child:apply" {
		t.Fatalf("activation order = %q", got)
	}
	if root.manifestCalls != 1 || mainChild.manifestCalls != 1 || commitChild.manifestCalls != 1 {
		t.Fatalf(
			"Manifest calls = root:%d main:%d commit:%d, want exactly once",
			root.manifestCalls,
			mainChild.manifestCalls,
			commitChild.manifestCalls,
		)
	}
	if len(runtimeEngine.Statuses()) != 3 {
		t.Fatalf("mounted statuses = %d, want complete tree", len(runtimeEngine.Statuses()))
	}
	if err := runtimeEngine.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown tree: %v", err)
	}
	if got := strings.Join(order, ","); got != "root:apply,main-child:apply,commit-child:apply,commit-child:dispose,main-child:dispose,root:dispose" {
		t.Fatalf("complete lifecycle order = %q", got)
	}
}

func TestRuntimeRejectsInvalidDescendantBeforeApply(t *testing.T) {
	invalid := &treePlugin{
		name: " invalid ",
	}
	root := &treePlugin{
		name: "root",
		children: []plugin.ChildPlugin{
			{
				Instance:  invalid,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), root); err == nil {
		t.Fatal("invalid descendant Start succeeded")
	}
	if root.applyCalls != 0 || invalid.applyCalls != 0 {
		t.Fatalf("Apply calls = root:%d invalid:%d, want none", root.applyCalls, invalid.applyCalls)
	}
}

func TestRuntimeRejectsDuplicateDeclaredPluginInstance(t *testing.T) {
	child := &treePlugin{
		name: "shared-child",
	}
	root := &treePlugin{
		name: "root",
		children: []plugin.ChildPlugin{
			{
				Instance:  child,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
			{
				Instance:  child,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		root,
	); err == nil || !strings.Contains(err.Error(), "already declared") {
		t.Fatalf("Start error = %v", err)
	}
	if root.applyCalls != 0 || child.applyCalls != 0 {
		t.Fatal("duplicate declaration was detected after Apply")
	}
}

func TestRuntimeRejectsDeclaredOwnershipCycle(t *testing.T) {
	root := &treePlugin{
		name: "cyclic-root",
	}
	root.children = []plugin.ChildPlugin{
		{
			Instance:  root,
			Placement: plugin.SameScope,
			Phase:     plugin.ActivationMain,
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		root,
	); err == nil || !strings.Contains(err.Error(), "ownership cycle") {
		t.Fatalf("Start error = %v", err)
	}
	if root.applyCalls != 0 {
		t.Fatal("ownership cycle was detected after Apply")
	}
}

func TestRuntimeRejectsServiceProviderInCommitPhase(t *testing.T) {
	provider := &clockProvider{
		value: "invalid",
	}
	root := &treePlugin{
		name: "root",
		children: []plugin.ChildPlugin{
			{
				Instance:  provider,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationCommit,
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		root,
	); err == nil || !strings.Contains(err.Error(), "cannot provide Services") {
		t.Fatalf("Start error = %v", err)
	}
	if root.applyCalls != 0 || provider.applications != 0 {
		t.Fatal("invalid commit provider was detected after Apply")
	}
}

func TestRuntimeDoesNotCommitFailedMainTree(t *testing.T) {
	order := make([]string, 0)
	failing := &treePlugin{
		name:     "failing-main",
		order:    &order,
		applyErr: errors.New("main failed"),
	}
	commitChild := &treePlugin{
		name:  "commit-child",
		order: &order,
	}
	root := &treePlugin{
		name:  "root",
		order: &order,
		children: []plugin.ChildPlugin{
			{
				Instance:  failing,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
			{
				Instance:  commitChild,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationCommit,
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), root); err == nil ||
		!strings.Contains(err.Error(), "main failed") {
		t.Fatalf("Start error = %v", err)
	}
	if commitChild.applyCalls != 0 {
		t.Fatalf("commit Apply calls = %d, want 0", commitChild.applyCalls)
	}
	if len(runtimeEngine.Statuses()) != 0 {
		t.Fatal("failed tree remained mounted")
	}
}

func TestRuntimeRejectsTreeServiceConflictBeforeApply(t *testing.T) {
	first := &clockProvider{
		value: "first",
	}
	second := &clockProvider{
		value: "second",
	}
	root := &treePlugin{
		name: "root",
		children: []plugin.ChildPlugin{
			{
				Instance:  first,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
			{
				Instance:  second,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), root); !errors.Is(
		err,
		plugin.ErrServiceConflict,
	) {
		t.Fatalf("Start error = %v", err)
	}
	if root.applyCalls != 0 || first.applications != 0 || second.applications != 0 {
		t.Fatal("service conflict was detected after Apply")
	}
}

func TestReplaceSwapsCompleteMainSubtree(t *testing.T) {
	firstProvider := &clockProvider{
		value: "v1",
	}
	firstRoot := &treePlugin{
		name: "replaceable-root",
		children: []plugin.ChildPlugin{
			{
				Instance:  firstProvider,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
	consumer := &clockConsumer{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), firstRoot, consumer)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	secondProvider := &clockProvider{
		value: "v2",
	}
	secondRoot := &treePlugin{
		name: "replaceable-root",
		children: []plugin.ChildPlugin{
			{
				Instance:  secondProvider,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
	if err = runtimeEngine.Replace(context.Background(), handles[0], secondRoot); err != nil {
		t.Fatalf("replace subtree: %v", err)
	}
	if consumer.applies != 2 || consumer.selected != secondProvider ||
		consumer.selected.Value() != "v2" {
		t.Fatal("external dependent did not move to the candidate subtree")
	}
	if firstRoot.disposeCalls != 1 || firstProvider.disposals != 1 {
		t.Fatalf(
			"previous subtree Dispose calls = root:%d child:%d",
			firstRoot.disposeCalls,
			firstProvider.disposals,
		)
	}
	if secondRoot.applyCalls != 1 || secondProvider.applications != 1 {
		t.Fatalf(
			"candidate subtree Apply calls = root:%d child:%d",
			secondRoot.applyCalls,
			secondProvider.applications,
		)
	}
}

func TestFailedCompositeReplacementKeepsCurrentSubtree(t *testing.T) {
	firstProvider := &clockProvider{
		value: "v1",
	}
	firstRoot := &treePlugin{
		name: "replaceable-root",
		children: []plugin.ChildPlugin{
			{
				Instance:  firstProvider,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
	consumer := &clockConsumer{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), firstRoot, consumer)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	failingProvider := &failingClockReplacement{}
	candidateRoot := &treePlugin{
		name: "replaceable-root",
		children: []plugin.ChildPlugin{
			{
				Instance:  failingProvider,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationMain,
			},
		},
	}
	if err = runtimeEngine.Replace(
		context.Background(),
		handles[0],
		candidateRoot,
	); err == nil || !strings.Contains(err.Error(), "replacement apply failed") {
		t.Fatalf("replace error = %v", err)
	}
	if consumer.applies != 1 || consumer.selected != firstProvider {
		t.Fatal("failed replacement disturbed the external dependent")
	}
	if firstRoot.disposeCalls != 0 || firstProvider.disposals != 0 {
		t.Fatal("failed replacement disposed the current subtree")
	}
	if candidateRoot.disposeCalls != 1 || failingProvider.disposals != 1 {
		t.Fatalf(
			"failed candidate Dispose calls = root:%d child:%d",
			candidateRoot.disposeCalls,
			failingProvider.disposals,
		)
	}
}

func TestReplaceRejectsCommitPhaseSubtree(t *testing.T) {
	commitChild := &treePlugin{
		name: "commit-child",
	}
	currentRoot := &treePlugin{
		name: "committed-root",
		children: []plugin.ChildPlugin{
			{
				Instance:  commitChild,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationCommit,
			},
		},
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), currentRoot)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	candidateCommit := &treePlugin{
		name: "commit-child",
	}
	candidateRoot := &treePlugin{
		name: "committed-root",
		children: []plugin.ChildPlugin{
			{
				Instance:  candidateCommit,
				Placement: plugin.SameScope,
				Phase:     plugin.ActivationCommit,
			},
		},
	}
	if err = runtimeEngine.Replace(
		context.Background(),
		handles[0],
		candidateRoot,
	); err == nil || !strings.Contains(err.Error(), "commit-phase") {
		t.Fatalf("replace error = %v", err)
	}
	if candidateRoot.applyCalls != 0 || candidateCommit.applyCalls != 0 {
		t.Fatal("rejected commit replacement invoked candidate Apply")
	}
}

type blockingEventObserver struct {
	plugin.Base
	entered  chan struct{}
	release  chan struct{}
	disposed chan struct{}
	once     sync.Once
}

func (*blockingEventObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "blocking-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[advanced](),
		},
	}
}

func (*blockingEventObserver) Apply(context.Context) error {
	return nil
}

func (observer *blockingEventObserver) Dispose(context.Context) error {
	observer.once.Do(func() {
		close(observer.disposed)
	})
	return nil
}

func (observer *blockingEventObserver) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	if _, matches := fact.(advanced); !matches {
		return errors.New("blocking Observer received an unexpected Event")
	}
	close(observer.entered)
	<-observer.release
	return nil
}

func TestUnloadWaitsForAdmittedEventObserver(t *testing.T) {
	publisher := &eventPublisherPlugin{
		name: "publisher",
	}
	observer := &blockingEventObserver{
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		disposed: make(chan struct{}),
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), publisher, observer)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	published := make(chan error, 1)
	go func() {
		published <- plugin.Publish(
			context.Background(),
			publisher,
			advanced{
				Value: 1,
			},
		)
	}()
	<-observer.entered
	unloaded := make(chan error, 1)
	go func() {
		unloaded <- runtimeEngine.Unload(context.Background(), handles[1])
	}()
	select {
	case err = <-unloaded:
		t.Fatalf("Unload returned before Observer completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-observer.disposed:
		t.Fatal("Observer was disposed while its callback was active")
	default:
	}
	close(observer.release)
	if err = <-published; err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err = <-unloaded; err != nil {
		t.Fatalf("unload: %v", err)
	}
	select {
	case <-observer.disposed:
	default:
		t.Fatal("Observer was not disposed after callback completion")
	}
}

type mutatingEventObserver struct {
	plugin.Base
	mutationErr error
}

func (*mutatingEventObserver) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "mutating-observer",
		Events: []plugin.EventSubscription{
			plugin.EventOf[advanced](),
		},
	}
}

func (*mutatingEventObserver) Apply(context.Context) error {
	return nil
}

func (*mutatingEventObserver) Dispose(context.Context) error {
	return nil
}

func (observer *mutatingEventObserver) ObserveEvent(
	requestContext context.Context,
	fact plugin.Event,
) error {
	if _, matches := fact.(advanced); !matches {
		return errors.New("mutating Observer received an unexpected Event")
	}
	_, observer.mutationErr = plugin.MountChild(
		requestContext,
		observer,
		&eventPublisherPlugin{
			name: "illegal-child",
		},
	)
	return nil
}

func TestDispatchCannotSynchronouslyMutateRuntimeTopology(t *testing.T) {
	publisher := &eventPublisherPlugin{
		name: "publisher",
	}
	observer := &mutatingEventObserver{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), publisher, observer); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := plugin.Publish(
		context.Background(),
		publisher,
		advanced{
			Value: 1,
		},
	); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !errors.Is(observer.mutationErr, plugin.ErrTopologyMutation) {
		t.Fatalf("topology mutation error = %v", observer.mutationErr)
	}
}
