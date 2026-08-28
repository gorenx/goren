package bound

import (
	"context"
	"errors"
	"strconv"
	"testing"

	boundcontract "github.com/gorenx/goren/subagent/bound"
)

func TestDefinitionCatalogPublishesOnlyCommittedRevisions(t *testing.T) {
	t.Parallel()
	persistence := newDefinitionStoreStub(t)
	persistence.load = []boundcontract.Definition{
		definitionFixture(t, "writer", 2, true),
		definitionFixture(t, "researcher", 1, true),
	}
	reconciler := &definitionReconcilerStub{
		calls: &persistence.calls,
	}
	catalog, err := newDefinitionCatalog(
		context.Background(),
		persistence,
		reconciler,
	)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 ||
		definitions[0].Name != "researcher" ||
		definitions[1].Name != "writer" {
		t.Fatalf("sorted Definitions = %#v", definitions)
	}
	created, err := catalog.Create(
		context.Background(),
		boundcontract.Creation{
			Definition: boundcontract.Draft{
				Name:         "reviewer",
				Enabled:      true,
				SystemPrompt: "review",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 {
		t.Fatalf("created revision = %d", created.Revision)
	}
	if len(persistence.calls) != 2 ||
		persistence.calls[0] != "create:reviewer:1" ||
		persistence.calls[1] != "reconcile" {
		t.Fatalf("Create call order = %#v", persistence.calls)
	}
	persistence.calls = nil
	replaced, err := catalog.Replace(
		context.Background(),
		boundcontract.Replacement{
			ExpectedRevision: 1,
			Definition: boundcontract.Draft{
				Name:         "reviewer",
				Enabled:      false,
				SystemPrompt: "review again",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Revision != 2 || replaced.Enabled {
		t.Fatalf("replaced Definition = %#v", replaced)
	}
	if len(persistence.calls) != 2 ||
		persistence.calls[0] != "replace:reviewer:1:2" ||
		persistence.calls[1] != "reconcile" {
		t.Fatalf("Replace call order = %#v", persistence.calls)
	}
}

func TestDefinitionCatalogDoesNotPublishFailedStoreWrite(t *testing.T) {
	t.Parallel()
	persistence := newDefinitionStoreStub(t)
	persistence.createErr = ErrDefinitionExists
	reconciler := &definitionReconcilerStub{
		calls: &persistence.calls,
	}
	catalog, err := newDefinitionCatalog(
		context.Background(),
		persistence,
		reconciler,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Create(
		context.Background(),
		boundcontract.Creation{
			Definition: boundcontract.Draft{
				Name:         "researcher",
				Enabled:      true,
				SystemPrompt: "research",
			},
		},
	)
	var typedError *boundcontract.Error
	if !errors.As(err, &typedError) ||
		typedError.Code != boundcontract.ErrorDefinitionExists {
		t.Fatalf("Create error = %#v", err)
	}
	definitions, listErr := catalog.List(context.Background())
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(definitions) != 0 {
		t.Fatalf("failed Definition was published: %#v", definitions)
	}
	if len(persistence.calls) != 1 ||
		persistence.calls[0] != "create:researcher:1" {
		t.Fatalf("failed Create calls = %#v", persistence.calls)
	}
}

func TestDefinitionCatalogReturnsDetachedValues(t *testing.T) {
	t.Parallel()
	persistence := newDefinitionStoreStub(t)
	catalog, err := newDefinitionCatalog(
		context.Background(),
		persistence,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := catalog.Create(
		context.Background(),
		boundcontract.Creation{
			Definition: boundcontract.Draft{
				Name:         "researcher",
				Enabled:      true,
				SystemPrompt: "research",
				Extensions:   []string{"report"},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	created.Extensions[0] = "changed"
	listed, err := catalog.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].Extensions[0] != "report" {
		t.Fatalf("catalog retained returned slice: %#v", listed[0])
	}
}

func TestDefinitionIndexDoesNotRegressAfterConcurrentStoreCommits(
	testingContext *testing.T,
) {
	testingContext.Parallel()
	index, err := newDefinitionIndex([]boundcontract.Definition{
		definitionFixture(testingContext, "researcher", 1, true),
	})
	if err != nil {
		testingContext.Fatal(err)
	}
	third := definitionFixture(testingContext, "researcher", 3, true)
	third.SystemPrompt = "revision three"
	second := definitionFixture(testingContext, "researcher", 2, true)
	second.SystemPrompt = "revision two"
	index.publish(third)
	index.publish(second)
	latest, found := index.find("researcher")
	if !found || latest.Revision != 3 ||
		latest.SystemPrompt != "revision three" {
		testingContext.Fatalf("latest Definition = %#v", latest)
	}
}

type definitionStoreStub struct {
	testingContext *testing.T
	load           []boundcontract.Definition
	createErr      error
	replaceErr     error
	calls          []string
	// Key is the stable Definition name. Value is the complete latest revision
	// accepted by this test Store.
	values map[string]boundcontract.Definition
}

func newDefinitionStoreStub(testingContext *testing.T) *definitionStoreStub {
	testingContext.Helper()
	return &definitionStoreStub{
		testingContext: testingContext,
		values:         make(map[string]boundcontract.Definition),
	}
}

func (storage *definitionStoreStub) Load(
	context.Context,
) ([]boundcontract.Definition, error) {
	return append([]boundcontract.Definition(nil), storage.load...), nil
}

func (storage *definitionStoreStub) Create(
	_ context.Context,
	definitionValue boundcontract.Definition,
) error {
	storage.calls = append(
		storage.calls,
		"create:"+definitionValue.Name+":"+
			strconv.FormatInt(definitionValue.Revision, 10),
	)
	if storage.createErr != nil {
		return storage.createErr
	}
	storage.values[definitionValue.Name] = definitionValue
	return nil
}

func (storage *definitionStoreStub) Replace(
	_ context.Context,
	expectedRevision int64,
	definitionValue boundcontract.Definition,
) error {
	storage.calls = append(
		storage.calls,
		"replace:"+definitionValue.Name+":"+
			strconv.FormatInt(expectedRevision, 10)+":"+
			strconv.FormatInt(definitionValue.Revision, 10),
	)
	if storage.replaceErr != nil {
		return storage.replaceErr
	}
	storage.values[definitionValue.Name] = definitionValue
	return nil
}

func (*definitionStoreStub) Close(context.Context) error {
	return nil
}

type definitionReconcilerStub struct {
	calls *[]string
}

func (reconciler *definitionReconcilerStub) DefinitionsChanged() {
	*reconciler.calls = append(*reconciler.calls, "reconcile")
}

func definitionFixture(
	testingContext *testing.T,
	definitionName string,
	revision int64,
	isEnabled bool,
) boundcontract.Definition {
	testingContext.Helper()
	definitionValue, err := boundcontract.NewDefinition(
		boundcontract.Draft{
			Name:         definitionName,
			Enabled:      isEnabled,
			SystemPrompt: definitionName + " prompt",
		},
		revision,
	)
	if err != nil {
		testingContext.Fatal(err)
	}
	return definitionValue
}
