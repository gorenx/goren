package workspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/workspace"
	workspaceSqlite "github.com/gorenx/goren/workspace/persistence/sqlite"
)

type headerSource struct {
	byID map[session.SessionID]session.Header
}

type unavailableHeaderSource struct{}

type concurrentHeaderSource struct {
	header  session.Header
	arrived sync.WaitGroup
	release chan struct{}
}

func newConcurrentHeaderSource(header session.Header) *concurrentHeaderSource {
	source := &concurrentHeaderSource{header: header, release: make(chan struct{})}
	source.arrived.Add(2)
	return source
}

func (source *concurrentHeaderSource) Get(
	context.Context,
	session.SessionID,
) (session.Header, bool, error) {
	source.arrived.Done()
	<-source.release
	return source.header, true, nil
}

func (*concurrentHeaderSource) List(context.Context) ([]session.Header, error) {
	return []session.Header{}, nil
}

func (unavailableHeaderSource) Get(
	context.Context,
	session.SessionID,
) (session.Header, bool, error) {
	return session.Header{}, false, errors.New("session headers unavailable")
}

func (unavailableHeaderSource) List(context.Context) ([]session.Header, error) {
	return nil, errors.New("session headers unavailable")
}

func (source *headerSource) Get(
	_ context.Context,
	identifier session.SessionID,
) (session.Header, bool, error) {
	header, found := source.byID[identifier]
	return header, found, nil
}

func (source *headerSource) List(context.Context) ([]session.Header, error) {
	result := make([]session.Header, 0, len(source.byID))
	for _, header := range source.byID {
		result = append(result, header)
	}
	return result, nil
}

type registryProvider struct {
	storage  workspace.Backend
	headers  workspace.SessionHeaders
	options  workspace.RegistryOptions
	registry workspace.Registry
}

func (*registryProvider) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "workspace-registry-test-provider"}
}

func (provider *registryProvider) Apply(requestContext context.Context, pluginScope *plugin.Scope) error {
	registry, err := workspace.NewRegistry(
		requestContext, pluginScope, provider.storage, provider.headers, provider.options,
	)
	if err != nil {
		return err
	}
	provider.registry = registry
	return nil
}

func TestRegistryPersistsCanonicalWorkspacesAndSessionAccounting(t *testing.T) {
	requestContext := context.Background()
	dataDirectory := t.TempDir()
	databasePath := filepath.Join(dataDirectory, "workspaces.sqlite")
	firstPath := filepath.Join(dataDirectory, "first")
	secondPath := filepath.Join(dataDirectory, "second")
	for _, directory := range []string{firstPath, secondPath} {
		if err := ensureDirectory(directory); err != nil {
			t.Fatal(err)
		}
	}
	firstCWD := firstPath
	secondCWD := firstPath
	headers := &headerSource{byID: map[session.SessionID]session.Header{}}
	identifiers := []workspace.ID{"workspace-1", "workspace-2"}
	identifierIndex := 0
	options := workspace.RegistryOptions{
		Clock: func() time.Time { return time.Unix(10, 0) },
		NewID: func() (workspace.ID, error) {
			identifier := identifiers[identifierIndex]
			identifierIndex++
			return identifier, nil
		},
	}
	storage, err := workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path: databasePath, JournalMode: workspaceSqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &registryProvider{storage: storage, headers: headers, options: options}
	engine := plugin.NewRuntime()
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	if listed := provider.registry.List(); len(listed) != 0 {
		t.Fatalf("initial workspaces = %#v", listed)
	}
	first, created, err := provider.registry.Create(requestContext, firstPath+"/../first")
	if err != nil || !created {
		t.Fatalf("create first = (%v, %t, %v)", first, created, err)
	}
	repeated, created, err := provider.registry.Create(requestContext, firstPath)
	if err != nil || created || repeated.Snapshot().ID != first.Snapshot().ID {
		t.Fatalf("repeat first = (%#v, %t, %v)", repeated.Snapshot(), created, err)
	}
	second, created, err := provider.registry.Create(requestContext, secondPath)
	if err != nil || !created {
		t.Fatalf("create second = (%v, %t, %v)", second, created, err)
	}
	headers.byID["session-1"] = session.Header{
		Version: session.FormatVersion, ID: "session-1", CreatedAt: 1, CWD: &firstCWD,
	}
	headers.byID["session-2"] = session.Header{
		Version: session.FormatVersion, ID: "session-2", CreatedAt: 2, CWD: &secondCWD,
	}
	if err := first.AttachSession(requestContext, "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := first.AttachSession(requestContext, "session-2"); err != nil {
		t.Fatal(err)
	}
	before := session.SessionID("session-2")
	if err := first.InsertSessionBefore(requestContext, "session-1", &before); err != nil {
		t.Fatal(err)
	}
	if got := first.Snapshot().SessionIDs; !reflect.DeepEqual(got, []session.SessionID{"session-1", "session-2"}) {
		t.Fatalf("first Session order = %#v", got)
	}
	if err := first.SetTitle(requestContext, "Primary"); err != nil {
		t.Fatal(err)
	}
	beforeWorkspace := first.Snapshot().ID
	order, err := provider.registry.InsertBefore(requestContext, second.Snapshot().ID, &beforeWorkspace)
	if err != nil || !reflect.DeepEqual(order, []workspace.ID{"workspace-2", "workspace-1"}) {
		t.Fatalf("workspace order = %#v, error = %v", order, err)
	}
	if err := provider.registry.ArchiveSession(requestContext, "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := provider.registry.ArchiveSession(requestContext, "missing"); err == nil {
		t.Fatal("unknown Session was archived")
	} else {
		var unknown *workspace.UnknownSessionError
		if !errors.As(err, &unknown) {
			t.Fatalf("archive error = %T %v", err, err)
		}
	}
	if err := engine.Shutdown(requestContext); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(requestContext); err != nil {
		t.Fatal(err)
	}

	reopened, err := workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path: databasePath, JournalMode: workspaceSqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(requestContext)
	restarted := &registryProvider{storage: reopened, headers: headers, options: options}
	restartEngine := plugin.NewRuntime()
	if _, err := restartEngine.Load(requestContext, restarted); err != nil {
		t.Fatal(err)
	}
	defer restartEngine.Shutdown(requestContext)
	listed := restarted.registry.List()
	if len(listed) != 2 || listed[0].Snapshot().ID != "workspace-2" || listed[1].Snapshot().Title != "Primary" {
		t.Fatalf("restarted workspaces = %#v", workspaceStates(listed))
	}
	if got := restarted.registry.ArchivedSessionIDs(); !reflect.DeepEqual(got, []session.SessionID{"session-1"}) {
		t.Fatalf("restarted archive set = %#v", got)
	}
}

func TestRegistryRejectsMismatchedSessionCWD(t *testing.T) {
	requestContext := context.Background()
	dataDirectory := t.TempDir()
	workspacePath := filepath.Join(dataDirectory, "workspace")
	foreignPath := filepath.Join(dataDirectory, "foreign")
	if err := ensureDirectory(workspacePath); err != nil {
		t.Fatal(err)
	}
	if err := ensureDirectory(foreignPath); err != nil {
		t.Fatal(err)
	}
	foreignCWD := foreignPath
	storage, err := workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path: filepath.Join(dataDirectory, "workspaces.sqlite"), JournalMode: workspaceSqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close(requestContext)
	headers := &headerSource{byID: map[session.SessionID]session.Header{}}
	provider := &registryProvider{
		storage: storage,
		headers: headers,
		options: workspace.RegistryOptions{NewID: func() (workspace.ID, error) { return "workspace-1", nil }},
	}
	engine := plugin.NewRuntime()
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(requestContext)
	subject, _, err := provider.registry.Create(requestContext, workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	headers.byID["foreign"] = session.Header{
		Version: session.FormatVersion, ID: "foreign", CreatedAt: 1, CWD: &foreignCWD,
	}
	if err := subject.AttachSession(requestContext, "foreign"); err == nil {
		t.Fatal("mismatched Session was attached")
	} else {
		var attach *workspace.AttachError
		if !errors.As(err, &attach) {
			t.Fatalf("attach error = %T %v", err, err)
		}
	}
	if got := subject.Snapshot().SessionIDs; len(got) != 0 {
		t.Fatalf("sessions after rejected attach = %#v", got)
	}
}

func TestRegistryDoesNotReadHeadersAfterInitializedEmptyBootstrap(t *testing.T) {
	requestContext := context.Background()
	storage, err := workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path: filepath.Join(t.TempDir(), "workspaces.sqlite"), JournalMode: workspaceSqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close(requestContext)
	if err := storage.Initialize(requestContext, workspace.StoredRegistry{
		Initialized: true, WorkspaceIDs: []workspace.ID{}, ArchivedSessionIDs: []session.SessionID{},
		Records: []workspace.StoredWorkspace{},
	}); err != nil {
		t.Fatal(err)
	}
	engine := plugin.NewRuntime()
	provider := &registryProvider{storage: storage, headers: unavailableHeaderSource{}}
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(requestContext)
	if len(provider.registry.List()) != 0 {
		t.Fatalf("initialized empty Workspaces = %#v", workspaceStates(provider.registry.List()))
	}
}

func TestRegistrySerializesConcurrentSessionAccountingAtCommit(t *testing.T) {
	requestContext := context.Background()
	dataDirectory := t.TempDir()
	workspacePath := filepath.Join(dataDirectory, "workspace")
	if err := ensureDirectory(workspacePath); err != nil {
		t.Fatal(err)
	}
	storage, err := workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path: filepath.Join(dataDirectory, "workspaces.sqlite"), JournalMode: workspaceSqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close(requestContext)
	canonicalPath, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	source := newConcurrentHeaderSource(session.Header{
		Version: session.FormatVersion, ID: "session-1", CreatedAt: 1, CWD: &canonicalPath,
	})
	provider := &registryProvider{
		storage: storage, headers: source,
		options: workspace.RegistryOptions{NewID: func() (workspace.ID, error) { return "workspace-1", nil }},
	}
	engine := plugin.NewRuntime()
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(requestContext)
	subject, _, err := provider.registry.Create(requestContext, canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	errorsByCall := make(chan error, 2)
	for range 2 {
		go func() { errorsByCall <- subject.AttachSession(requestContext, "session-1") }()
	}
	source.arrived.Wait()
	close(source.release)
	for range 2 {
		if err := <-errorsByCall; err != nil {
			t.Fatal(err)
		}
	}
	if got := subject.Snapshot().SessionIDs; !reflect.DeepEqual(got, []session.SessionID{"session-1"}) {
		t.Fatalf("concurrently attached Session IDs = %#v", got)
	}
}

func TestRegistryBootstrapMatchesHistoricalSessionAndWorkspaceOrder(t *testing.T) {
	requestContext := context.Background()
	dataDirectory := t.TempDir()
	pathA := filepath.Join(dataDirectory, "a")
	pathB := filepath.Join(dataDirectory, "b")
	pathC := filepath.Join(dataDirectory, "c")
	for _, directory := range []string{pathA, pathB, pathC} {
		if err := ensureDirectory(directory); err != nil {
			t.Fatal(err)
		}
	}
	pathA, err := filepath.EvalSymlinks(pathA)
	if err != nil {
		t.Fatal(err)
	}
	pathB, err = filepath.EvalSymlinks(pathB)
	if err != nil {
		t.Fatal(err)
	}
	pathC, err = filepath.EvalSymlinks(pathC)
	if err != nil {
		t.Fatal(err)
	}
	storage, err := workspaceSqlite.Open(requestContext, workspaceSqlite.Config{
		Path: filepath.Join(dataDirectory, "workspaces.sqlite"), JournalMode: workspaceSqlite.JournalWAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer storage.Close(requestContext)
	initialTime := time.UnixMilli(1_000).UTC()
	if err := storage.Initialize(requestContext, workspace.StoredRegistry{
		Initialized:  false,
		WorkspaceIDs: []workspace.ID{"workspace-b", "workspace-a"},
		Records: []workspace.StoredWorkspace{
			{
				ID: "workspace-a", Path: pathA, Title: "", SessionIDs: []session.SessionID{"manual", "a-old"},
				CreatedAt: initialTime, UpdatedAt: initialTime,
			},
			{
				ID: "workspace-b", Path: pathB, Title: "B", SessionIDs: []session.SessionID{},
				CreatedAt: initialTime, UpdatedAt: initialTime,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	headers := &headerSource{byID: map[session.SessionID]session.Header{
		"a-new": {Version: session.FormatVersion, ID: "a-new", CreatedAt: 5_000, CWD: &pathA},
		"a-old": {Version: session.FormatVersion, ID: "a-old", CreatedAt: 4_000, CWD: &pathA},
		"b":     {Version: session.FormatVersion, ID: "b", CreatedAt: 5_000, CWD: &pathB},
		"c":     {Version: session.FormatVersion, ID: "c", CreatedAt: 6_000, CWD: &pathC},
	}}
	provider := &registryProvider{
		storage: storage,
		headers: headers,
		options: workspace.RegistryOptions{
			Clock: func() time.Time { return time.UnixMilli(7_000).UTC() },
			NewID: func() (workspace.ID, error) { return "workspace-c", nil },
		},
	}
	engine := plugin.NewRuntime()
	if _, err := engine.Load(requestContext, provider); err != nil {
		t.Fatal(err)
	}
	defer engine.Shutdown(requestContext)

	listed := workspaceStates(provider.registry.List())
	if len(listed) != 3 {
		t.Fatalf("bootstrapped Workspaces = %#v", listed)
	}
	if got := []workspace.ID{listed[0].ID, listed[1].ID, listed[2].ID}; !reflect.DeepEqual(got, []workspace.ID{"workspace-c", "workspace-b", "workspace-a"}) {
		t.Fatalf("bootstrapped Workspace order = %#v", got)
	}
	if got := listed[2].SessionIDs; !reflect.DeepEqual(got, []session.SessionID{"a-new", "a-old"}) {
		t.Fatalf("filtered Workspace A sessions = %#v", got)
	}
	stored, err := storage.Load(requestContext)
	if err != nil {
		t.Fatal(err)
	}
	records := make(map[workspace.ID]workspace.StoredWorkspace, len(stored.Records))
	for _, storedRecord := range stored.Records {
		records[storedRecord.ID] = storedRecord
	}
	if got := records["workspace-a"].SessionIDs; !reflect.DeepEqual(got, []session.SessionID{"a-new", "a-old", "manual"}) {
		t.Fatalf("durable Workspace A candidates = %#v", got)
	}
	if !records["workspace-a"].UpdatedAt.Equal(time.UnixMilli(7_000).UTC()) {
		t.Fatalf("Workspace A updatedAt = %s", records["workspace-a"].UpdatedAt)
	}
	if got := records["workspace-c"].SessionIDs; !reflect.DeepEqual(got, []session.SessionID{"c"}) {
		t.Fatalf("durable Workspace C sessions = %#v", got)
	}
	if !records["workspace-c"].CreatedAt.Equal(time.UnixMilli(6_000).UTC()) {
		t.Fatalf("Workspace C createdAt = %s", records["workspace-c"].CreatedAt)
	}
}

func ensureDirectory(directory string) error {
	return os.MkdirAll(directory, 0o755)
}

func workspaceStates(items []workspace.Workspace) []workspace.WorkspaceState {
	result := make([]workspace.WorkspaceState, 0, len(items))
	for _, subject := range items {
		result = append(result, subject.Snapshot())
	}
	return result
}
