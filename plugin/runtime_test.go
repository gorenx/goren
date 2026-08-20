package plugin_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/gorenx/goren/plugin"
)

type clock interface {
	plugin.Service
	Value() string
}

type clockProvider struct {
	plugin.Base
	value     string
	order     *[]string
	disposals int
}

func (provider *clockProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "clock",
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[clock](),
		},
	}
}

func (provider *clockProvider) Apply(context.Context) error {
	if provider.order != nil {
		*provider.order = append(*provider.order, "clock:apply")
	}
	return nil
}

func (provider *clockProvider) Dispose(context.Context) error {
	provider.disposals++
	if provider.order != nil {
		*provider.order = append(*provider.order, "clock:dispose")
	}
	return nil
}

func (provider *clockProvider) Value() string {
	return provider.value
}

type clockConsumer struct {
	plugin.Base
	selected clock
	order    *[]string
	applies  int
}

func (consumer *clockConsumer) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "clock-consumer",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[clock](),
		},
	}
}

func (consumer *clockConsumer) Apply(context.Context) error {
	selected, err := plugin.Require[clock](consumer)
	if err != nil {
		return err
	}
	consumer.selected = selected
	consumer.applies++
	if consumer.order != nil {
		*consumer.order = append(*consumer.order, "consumer:apply")
	}
	return nil
}

func (consumer *clockConsumer) Dispose(context.Context) error {
	consumer.selected = nil
	if consumer.order != nil {
		*consumer.order = append(*consumer.order, "consumer:dispose")
	}
	return nil
}

type failingPlugin struct {
	plugin.Base
	disposals int
}

func (*failingPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "failing",
	}
}

func (*failingPlugin) Apply(context.Context) error {
	return errors.New("apply failed")
}

func (candidate *failingPlugin) Dispose(context.Context) error {
	candidate.disposals++
	return nil
}

type optionalClockConsumer struct {
	plugin.Base
	selected clock
	applies  int
}

func (*optionalClockConsumer) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "optional-clock-consumer",
		Optional: []plugin.ServiceType{
			plugin.ServiceOf[clock](),
		},
	}
}

func (consumer *optionalClockConsumer) Apply(context.Context) error {
	consumer.selected, _ = plugin.Resolve[clock](consumer)
	consumer.applies++
	return nil
}

func (consumer *optionalClockConsumer) Dispose(context.Context) error {
	consumer.selected = nil
	return nil
}

func TestRuntimeDerivesStableServiceIdentityFromGoType(t *testing.T) {
	t.Parallel()
	leftType := plugin.ServiceOf[clock]()
	rightType := plugin.ServiceOf[clock]()
	if leftType.Name() != rightType.Name() {
		t.Fatalf("same Go interface produced different names: %q != %q", leftType.Name(), rightType.Name())
	}
	if !strings.Contains(leftType.Name(), ".clock") {
		t.Fatalf("service name %q does not identify the business interface", leftType.Name())
	}
}

func TestRuntimeRejectsManifestServiceNotImplementedByPlugin(t *testing.T) {
	t.Parallel()
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		&invalidClockProvider{},
	); err == nil || !strings.Contains(err.Error(), "does not implement") {
		t.Fatalf("Start error = %v", err)
	}
}

type invalidClockProvider struct {
	plugin.Base
}

func (*invalidClockProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "invalid-clock",
		Provides: []plugin.ServiceType{
			plugin.ServiceOf[clock](),
		},
	}
}

func (*invalidClockProvider) Apply(context.Context) error {
	return nil
}

func (*invalidClockProvider) Dispose(context.Context) error {
	return nil
}

func TestRuntimeStartsProvidersBeforeConsumersWithoutExplicitProvide(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	provider := &clockProvider{
		value: "root",
		order: &order,
	}
	consumer := &clockConsumer{
		order: &order,
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		consumer,
		provider,
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(handles) != 2 {
		t.Fatalf("got %d handles, want 2", len(handles))
	}
	if consumer.selected == nil || consumer.selected.Value() != "root" {
		t.Fatal("consumer did not receive the declared Service")
	}
	if got := strings.Join(order, ","); got != "clock:apply,consumer:apply" {
		t.Fatalf("activation order = %q", got)
	}
	if err := runtimeEngine.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if got := strings.Join(order, ","); got != "clock:apply,consumer:apply,consumer:dispose,clock:dispose" {
		t.Fatalf("shutdown order = %q", got)
	}
}

func TestRuntimeRollsBackFailedStartAndAllowsRetry(t *testing.T) {
	t.Parallel()
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	consumer := &clockConsumer{}
	if _, err := runtimeEngine.Start(context.Background(), consumer); err == nil {
		t.Fatal("missing dependency Start succeeded")
	}
	provider := &clockProvider{
		value: "retry",
	}
	if _, err := runtimeEngine.Start(
		context.Background(),
		consumer,
		provider,
	); err != nil {
		t.Fatalf("retry Start: %v", err)
	}
	if consumer.selected == nil || consumer.selected.Value() != "retry" {
		t.Fatal("retry did not resolve Service")
	}
}

func TestApplyFailureDisposesPartialPluginAndBatch(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	provider := &clockProvider{
		value: "ready",
		order: &order,
	}
	failing := &failingPlugin{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(
		context.Background(),
		provider,
		failing,
	); err == nil {
		t.Fatal("failed Apply did not fail Start")
	}
	if failing.disposals != 1 {
		t.Fatalf("partial Plugin Dispose calls = %d, want 1", failing.disposals)
	}
	if provider.disposals != 1 {
		t.Fatalf("activated Plugin Dispose calls = %d, want 1", provider.disposals)
	}
}

func TestRequireIsOnlyAvailableDuringApply(t *testing.T) {
	t.Parallel()
	provider := &clockProvider{
		value: "ready",
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	if _, err := runtimeEngine.Start(context.Background(), provider); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := plugin.Require[clock](provider); !errors.Is(
		err,
		plugin.ErrDependencyResolutionClosed,
	) {
		t.Fatalf("Require outside Apply error = %v", err)
	}
}

func TestChildScopeInheritsAndOverridesService(t *testing.T) {
	t.Parallel()
	rootProvider := &clockProvider{
		value: "root",
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	rootHandles, err := runtimeEngine.Start(context.Background(), rootProvider)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	inherited := &clockConsumer{}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		rootHandles[0],
		inherited,
	); err != nil {
		t.Fatalf("mount inherited consumer: %v", err)
	}
	if inherited.selected.Value() != "root" {
		t.Fatalf("inherited Service = %q", inherited.selected.Value())
	}

	override := &clockProvider{
		value: "child",
	}
	overrideHandle, err := runtimeEngine.MountChild(
		context.Background(),
		rootHandles[0],
		override,
	)
	if err != nil {
		t.Fatalf("mount override: %v", err)
	}
	overridden := &clockConsumer{}
	if _, err := runtimeEngine.MountChild(
		context.Background(),
		overrideHandle,
		overridden,
	); err != nil {
		t.Fatalf("mount overridden consumer: %v", err)
	}
	if overridden.selected.Value() != "child" {
		t.Fatalf("overridden Service = %q", overridden.selected.Value())
	}
}

func TestReplaceReactivatesHardDependents(t *testing.T) {
	t.Parallel()
	firstProvider := &clockProvider{
		value: "v1",
	}
	consumer := &clockConsumer{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		firstProvider,
		consumer,
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	replacement := &clockProvider{
		value: "v2",
	}
	if err := runtimeEngine.Replace(
		context.Background(),
		handles[0],
		replacement,
	); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if consumer.applies != 2 {
		t.Fatalf("consumer Apply calls = %d, want 2", consumer.applies)
	}
	if consumer.selected == nil || consumer.selected.Value() != "v2" {
		t.Fatal("consumer retained the replaced provider")
	}
	if firstProvider.disposals != 1 {
		t.Fatalf("previous provider Dispose calls = %d, want 1", firstProvider.disposals)
	}
}

func TestReplaceReactivatesResolvedOptionalDependents(t *testing.T) {
	t.Parallel()
	firstProvider := &clockProvider{
		value: "v1",
	}
	consumer := &optionalClockConsumer{}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(
		context.Background(),
		firstProvider,
		consumer,
	)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	replacement := &clockProvider{
		value: "v2",
	}
	if err := runtimeEngine.Replace(
		context.Background(),
		handles[0],
		replacement,
	); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if consumer.applies != 2 {
		t.Fatalf("optional consumer Apply calls = %d, want 2", consumer.applies)
	}
	if consumer.selected == nil || consumer.selected.Value() != "v2" {
		t.Fatal("optional consumer retained the replaced provider")
	}
}

func TestLifetimeIsCancelledBeforeDispose(t *testing.T) {
	t.Parallel()
	cancellationObserved := make(chan struct{}, 1)
	lifecycle := &lifetimePlugin{
		cancellationObserved: cancellationObserved,
	}
	runtimeEngine := plugin.NewRuntime(plugin.RuntimeSettings{})
	handles, err := runtimeEngine.Start(context.Background(), lifecycle)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := runtimeEngine.Unload(context.Background(), handles[0]); err != nil {
		t.Fatalf("unload: %v", err)
	}
	select {
	case <-cancellationObserved:
	default:
		t.Fatal("Dispose did not observe a cancelled lifetime")
	}
}

type lifetimePlugin struct {
	plugin.Base
	cancellationObserved chan<- struct{}
	once                 sync.Once
}

func (*lifetimePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: "lifetime",
	}
}

func (*lifetimePlugin) Apply(context.Context) error {
	return nil
}

func (lifecycle *lifetimePlugin) Dispose(context.Context) error {
	if plugin.Lifetime(lifecycle).Err() != nil {
		lifecycle.once.Do(func() {
			lifecycle.cancellationObserved <- struct{}{}
		})
	}
	return nil
}
