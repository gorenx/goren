package toolruntime_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	toolruntime "github.com/gorenx/goren/tools/runtime"
	"github.com/gorenx/goren/tools/tool"
)

var errInjected = errors.New("injected failure")

type unusedProvider struct{}

func (unusedProvider) Open(context.Context) (tool.Implementation, error) {
	panic("instance port owns construction")
}

type catalogPort struct {
	business             toolregistry.Registry
	publicationLost      bool
	lookupFailure        bool
	settlementFailure    bool
	publicationRejected  bool
	abortFailure         bool
	registrationLost     bool
	registrationRejected bool
}

func (port *catalogPort) Configure(_ context.Context, identifier toolregistry.UpdateID, agentID session.SessionID, overrides []toolregistry.Tool) (toolregistry.Configuration, error) {
	if port.registrationRejected {
		return toolregistry.Configuration{}, errInjected
	}
	_, err := port.business.Configure(identifier, agentID, overrides)
	if err != nil {
		return toolregistry.Configuration{}, err
	}
	if port.registrationLost {
		return toolregistry.Configuration{}, errInjected
	}
	configured, _ := port.business.ReadConfiguration(identifier)
	return configured, nil
}

func (port *catalogPort) Publish(_ context.Context, identifier toolregistry.UpdateID) (tools.Definitions, error) {
	if port.publicationRejected {
		return tools.Definitions{}, errInjected
	}
	fact, err := port.business.Publish(identifier)
	if port.publicationLost {
		return tools.Definitions{}, errInjected
	}
	return fact, err
}

func (port *catalogPort) Lookup(_ context.Context, identifier toolregistry.UpdateID) (toolregistry.Publication, bool, error) {
	if port.lookupFailure {
		return toolregistry.Publication{}, false, errInjected
	}
	fact, found := port.business.Lookup(identifier)
	return fact, found, nil
}

func (port *catalogPort) Abort(_ context.Context, identifier toolregistry.UpdateID) error {
	if port.abortFailure {
		return errInjected
	}
	return port.business.Abort(identifier)
}

func (port *catalogPort) Settle(_ context.Context, identifier toolregistry.UpdateID) error {
	if port.settlementFailure {
		return errInjected
	}
	return port.business.Settle(identifier)
}

type instancePort struct {
	directory              *catalogPort
	operations             []string
	fail                   string
	cleanupFailure         string
	cancelAfterPreparation context.CancelFunc
}

func (port *instancePort) record(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	port.operations = append(port.operations, operation)
	if operation == port.fail || operation == port.cleanupFailure {
		return errInjected
	}
	return nil
}

func (port *instancePort) Prepare(ctx context.Context, target tool.Revision, entry toolregistry.Tool) error {
	if err := port.record(ctx, "prepare:"+entry.Name); err != nil {
		return err
	}
	err := port.directory.business.Register(toolregistry.UpdateID(string(target)), toolregistry.Candidate{
		Schema: llm.ToolSchema{
			Name:       entry.Name,
			Parameters: []byte(`{"type":"object"}`),
		},
		Visible: entry.Visible,
	})
	if port.cancelAfterPreparation != nil {
		port.cancelAfterPreparation()
	}
	return err
}

func (port *instancePort) Commit(ctx context.Context, target tool.Revision, name string) error {
	fact, found := port.directory.business.Lookup(toolregistry.UpdateID(string(target)))
	if !found || !fact.Published {
		return errors.New("commit before publication")
	}
	return port.record(ctx, "commit:"+name)
}

func (port *instancePort) Retire(ctx context.Context, _ tool.Revision, name string) error {
	return port.record(ctx, "retire:"+name)
}

func (port *instancePort) Abort(ctx context.Context, target tool.Revision, name string) error {
	fact, _ := port.directory.business.Lookup(toolregistry.UpdateID(string(target)))
	if fact.Published {
		return errors.New("abort after publication")
	}
	return port.record(ctx, "abort:"+name)
}

func (port *instancePort) Remove(ctx context.Context, name string) error {
	return port.record(ctx, "remove:"+name)
}

func newUpdate(t *testing.T) (*toolruntime.Update, *catalogPort, *instancePort) {
	t.Helper()
	directory := &catalogPort{}
	if err := directory.business.Begin(toolregistry.Update{
		ID:      "previous",
		AgentID: "agent",
		Names:   []string{"kept", "removed"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"kept", "removed"} {
		if err := directory.business.Register("previous", toolregistry.Candidate{
			Schema: llm.ToolSchema{
				Name:       name,
				Parameters: []byte(`{"type":"object"}`),
			},
			Visible: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := directory.business.Publish("previous"); err != nil {
		t.Fatal(err)
	}
	if err := directory.business.Settle("previous"); err != nil {
		t.Fatal(err)
	}
	if err := directory.business.InitializeDefaults([]toolregistry.Tool{
		{
			Name:     "kept",
			Provider: unusedProvider{},
			Visible:  true,
		},
		{
			Name:     "added",
			Provider: unusedProvider{},
			Visible:  true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.business.Configure("update", "agent", nil); err != nil {
		t.Fatal(err)
	}
	configuration, _ := directory.business.ReadConfiguration("update")
	operation, err := toolruntime.NewUpdate(configuration, []string{"kept", "removed"})
	if err != nil {
		t.Fatal(err)
	}
	return operation, directory, &instancePort{
		directory: directory,
	}
}

func TestUpdatePublishesBeforeSwitchingAndSettlesAfterCleanup(t *testing.T) {
	operation, directory, instances := newUpdate(t)
	complete, err := operation.Apply(context.Background(), directory, instances)
	if !complete || err != nil {
		t.Fatalf("update = %v, %v", complete, err)
	}
	expected := []string{"prepare:kept", "prepare:added", "commit:kept", "commit:added", "retire:kept", "retire:added", "remove:removed"}
	if !slices.Equal(instances.operations, expected) {
		t.Fatalf("unsafe update order: %v", instances.operations)
	}
	if _, found := directory.business.Lookup("update"); found {
		t.Fatal("successful update was not settled")
	}
	if complete, err := operation.Apply(context.Background(), directory, instances); !complete || err != nil || !slices.Equal(instances.operations, expected) {
		t.Fatal("completed update repeated resource operations")
	}
}

func TestPreparationFailureWaitsForRollbackAndPreservesExistingTools(t *testing.T) {
	operation, directory, instances := newUpdate(t)
	instances.fail = "prepare:added"
	complete, err := operation.Apply(context.Background(), directory, instances)
	if !complete || !errors.Is(err, errInjected) {
		t.Fatalf("rollback = %v, %v", complete, err)
	}
	expected := []string{"prepare:kept", "prepare:added", "abort:kept", "abort:added", "remove:added"}
	if !slices.Equal(instances.operations, expected) {
		t.Fatalf("rollback removed old tools or omitted failing candidate: %v", instances.operations)
	}
	if _, found := directory.business.Lookup("update"); found {
		t.Fatal("rollback retained the batch")
	}
	if fact, found := directory.business.Read("agent"); !found || len(fact.Schemas) != 2 || fact.Schemas[0].Version != 1 || !slices.Equal(fact.KnownNames, []string{"kept", "removed"}) {
		t.Fatal("preparation failure changed committed definitions")
	}
}

func TestRollbackFailuresRetainBarrierAndResumeCleanup(t *testing.T) {
	for _, failure := range []string{"abort:kept", "remove:added", "catalog"} {
		t.Run(failure, func(t *testing.T) {
			operation, directory, instances := newUpdate(t)
			instances.fail = "prepare:added"
			instances.cleanupFailure = failure
			directory.abortFailure = failure == "catalog"
			complete, err := operation.Apply(context.Background(), directory, instances)
			if complete || !errors.Is(err, errInjected) {
				t.Fatalf("unfinished cleanup released barrier: %v, %v", complete, err)
			}
			if _, found := directory.business.Lookup("update"); !found {
				t.Fatal("batch discarded before rollback cleanup")
			}
			instances.cleanupFailure = ""
			directory.abortFailure = false
			complete, err = operation.Apply(context.Background(), directory, instances)
			if !complete || !errors.Is(err, errInjected) || count(instances.operations, "prepare:kept") != 1 {
				t.Fatalf("rollback retry = %v, %v, %v", complete, err, instances.operations)
			}
		})
	}
}

func TestRejectedPublicationRollsBackOnlyAfterLookup(t *testing.T) {
	operation, directory, instances := newUpdate(t)
	directory.publicationRejected = true
	complete, err := operation.Apply(context.Background(), directory, instances)
	if !complete || !errors.Is(err, errInjected) || !slices.Contains(instances.operations, "abort:added") || slices.Contains(instances.operations, "commit:kept") {
		t.Fatalf("rejected publication = %v, %v, %v", complete, err, instances.operations)
	}
	fact, found := directory.business.Read("agent")
	if !found || len(fact.Schemas) != 2 || fact.Schemas[0].Version != 1 {
		t.Fatal("rejected publication changed the old catalog")
	}
}

func TestLostPublicationReplyRetainsBarrierUntilExactLookupSucceeds(t *testing.T) {
	operation, directory, instances := newUpdate(t)
	directory.publicationLost = true
	directory.lookupFailure = true
	complete, err := operation.Apply(context.Background(), directory, instances)
	if complete || !errors.Is(err, errInjected) || len(instances.operations) != 2 {
		t.Fatalf("unknown publication released barrier: %v, %v, %v", complete, err, instances.operations)
	}
	directory.lookupFailure = false
	complete, err = operation.Apply(context.Background(), directory, instances)
	if !complete || err != nil || slices.Contains(instances.operations, "abort:kept") {
		t.Fatalf("published update was rolled back: %v, %v, %v", complete, err, instances.operations)
	}
}

func TestPostPublicationFailuresResumeWithoutRepeatingSuccessfulSteps(t *testing.T) {
	for _, failure := range []string{"commit:added", "retire:added", "remove:removed", "settlement"} {
		t.Run(failure, func(t *testing.T) {
			operation, directory, instances := newUpdate(t)
			instances.fail = failure
			directory.settlementFailure = failure == "settlement"
			complete, err := operation.Apply(context.Background(), directory, instances)
			if complete || !errors.Is(err, errInjected) {
				t.Fatalf("failure prematurely completed update: %v, %v", complete, err)
			}
			before := slices.Clone(instances.operations)
			instances.fail = ""
			directory.settlementFailure = false
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			complete, err = operation.Apply(ctx, directory, instances)
			if !complete || err != nil {
				t.Fatalf("cancelled post-publication retry = %v, %v", complete, err)
			}
			for _, step := range before {
				if step != failure && count(instances.operations, step) != 1 {
					t.Fatalf("successful step repeated: %s, %v", step, instances.operations)
				}
			}
		})
	}
}

func TestCancellationBeforePublicationCleansPreparedCandidates(t *testing.T) {
	operation, directory, instances := newUpdate(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instances.cancelAfterPreparation = cancel
	complete, err := operation.Apply(ctx, directory, instances)
	if !complete || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v, %v", complete, err)
	}
	if !slices.Equal(instances.operations, []string{"prepare:kept", "abort:kept", "remove:added"}) {
		t.Fatalf("cancelled preparation cleanup = %v", instances.operations)
	}
}

func count(entries []string, wanted string) int {
	matched := 0
	for _, entry := range entries {
		if entry == wanted {
			matched++
		}
	}
	return matched
}
