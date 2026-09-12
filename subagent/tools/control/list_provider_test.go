package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
	"github.com/gorenx/goren/tools"
)

type listingReader struct {
	input   Listing
	context context.Context
	calls   int
	rows    []Entry
	failure error
}

func (source *listingReader) List(ctx context.Context, input Listing) ([]Entry, error) {
	source.input = input
	source.context = ctx
	source.calls++
	return source.rows, source.failure
}

func TestListProviderSharesExecutionRenderingAndCallerContext(t *testing.T) {
	source := &listingReader{
		rows: []Entry{
			{
				Kind:   "child",
				ID:     "child",
				Label:  "reviewer",
				Status: "starting",
			},
		},
	}
	provider, err := NewListProvider(source)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := provider.Open(t.Context())
	if err != nil || source.calls != 0 || opened.Resource != nil {
		t.Fatalf("Open performed directory work or acquired resources: %v", err)
	}
	ctx := tools.WithAgentCreationID(t.Context(), "current-parent")
	run := tools.ToolRunContext{
		Context: ctx,
		Execution: tools.Invocation{
			SessionID: "parent",
		},
	}
	encoded, err := opened.Definition.Executor.Execute(json.RawMessage(`{}`), run)
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || source.input.ParentSessionID != "parent" || source.input.Scope != "children" || source.context != ctx {
		t.Fatalf("caller context changed: %+v", source.input)
	}
	var rows []Entry
	if err := json.Unmarshal(encoded, &rows); err != nil || len(rows) != 1 || rows[0].Status != "starting" {
		t.Fatalf("output=%s error=%v", encoded, err)
	}
	blocks, err := opened.Definition.Output.Renderer.Render(json.RawMessage(`{}`), encoded)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _ := json.Marshal(blocks)
	if !strings.Contains(string(rendered), "child [starting]") || !strings.Contains(string(rendered), "reviewer") {
		t.Fatalf("shared renderer lost lifecycle information: %s", rendered)
	}
	source.rows = nil
	encoded, err = opened.Definition.Executor.Execute(json.RawMessage(`{}`), run)
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("empty listing=%s error=%v", encoded, err)
	}
	source.failure = errors.New("directory unavailable")
	if _, err := opened.Definition.Executor.Execute(json.RawMessage(`{}`), run); !errors.Is(err, source.failure) {
		t.Fatalf("directory error lost: %v", err)
	}
	before := source.calls
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	run.Context = canceled
	if _, err := opened.Definition.Executor.Execute(json.RawMessage(`{}`), run); !errors.Is(err, context.Canceled) || source.calls != before {
		t.Fatalf("canceled execution reached source: %v", err)
	}
}

func TestListProviderSchemasAreDetachedAndHaveNoScope(t *testing.T) {
	if _, err := NewListProvider(nil); err == nil {
		t.Fatal("missing Lister accepted")
	}
	provider, err := NewListProvider(&listingReader{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Open(nil); err == nil {
		t.Fatal("nil context accepted")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := provider.Open(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Open: %v", err)
	}
	first, err := provider.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first.Definition.Parameters[0] = 'x'
	first.Definition.Output.Schema[0] = 'x'
	initial := ListSchema()
	initial.Parameters[0] = 'x'
	second, err := provider.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(second.Definition.Parameters) || !json.Valid(second.Definition.Output.Schema) || !json.Valid(ListSchema().Parameters) {
		t.Fatal("schema mutation escaped its owner")
	}
	var parameters struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(second.Definition.Parameters, &parameters); err != nil {
		t.Fatal(err)
	}
	if len(parameters.Properties) != 0 {
		t.Fatal("direct Companion tool acquired model routing parameters")
	}
}

type listingDirectory struct {
	parent session.SessionID
}

func (*listingDirectory) ListChildren(context.Context, session.SessionID) ([]subagent.ChildEntry, error) {
	return nil, nil
}
func (source *listingDirectory) ListDescendants(_ context.Context, parent session.SessionID) ([]subagent.DescendantEntry, error) {
	source.parent = parent
	return []subagent.DescendantEntry{
		{
			Entry: subagent.ContinuableChildEntry{
				ID:    "grandchild",
				Label: "worker",
			},
			ParentID: "child",
			Depth:    2,
		},
		{
			Entry: subagent.OneShotChildEntry{
				ID: "oneshot",
			},
			ParentID: "parent",
			Depth:    1,
		},
	}, nil
}

func TestExistingControlListingRetainsDescendantTraversal(t *testing.T) {
	directory := &listingDirectory{}
	owner, err := newControlTools(&messageRecord{}, &childControlRecord{}, directory, activityRecord{})
	if err != nil {
		t.Fatal(err)
	}
	definition := owner.listDefinition()
	encoded, err := definition.Executor.Execute(json.RawMessage(`{"scope":"descendants"}`), tools.ToolRunContext{
		Context: t.Context(),
		Execution: tools.Invocation{
			SessionID: "parent",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows []Entry
	if err := json.Unmarshal(encoded, &rows); err != nil {
		t.Fatal(err)
	}
	if directory.parent != "parent" || len(rows) != 1 || rows[0].ID != "grandchild" || rows[0].Status != "ready" || rows[0].Parent == nil || *rows[0].Parent != "child" || rows[0].Depth == nil || *rows[0].Depth != 2 {
		t.Fatalf("legacy traversal or OneShot filtering changed: %+v", rows)
	}
	blocks, err := definition.Output.Renderer.Render(json.RawMessage(`{"scope":"descendants"}`), encoded)
	if err != nil {
		t.Fatal(err)
	}
	expected := []agentmessage.ContentBlock{agentmessage.NewTextBlock("grandchild [ready] parent=child depth=2 — worker")}
	actual, _ := json.Marshal(blocks)
	desired, _ := json.Marshal(expected)
	if string(actual) != string(desired) {
		t.Fatalf("legacy presentation changed: %s", actual)
	}
}
