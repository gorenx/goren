package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gorenx/goren/agentmessage"
	"github.com/gorenx/goren/tools"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
)

type body struct{}

func (body) Execute(json.RawMessage, tools.ToolRunContext) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func (body) Render(json.RawMessage, json.RawMessage) ([]agentmessage.ContentBlock, error) {
	return nil, nil
}

type resource struct {
	calls int
	err   error
}

func (owned *resource) Close(context.Context) error {
	owned.calls++
	return owned.err
}

func implementation(owned tool.Resource) tool.Implementation {
	return tool.Implementation{
		Definition: tools.ToolDefinition{
			Name:       "lookup",
			Parameters: json.RawMessage(`{"type":"object"}`),
			Output: tools.ToolOutputDefinition{
				Schema:   json.RawMessage(`{"type":"object"}`),
				Renderer: body{},
			},
			Executor: body{},
		},
		Resource: owned,
	}
}

func newTool(t *testing.T) *tool.Tool {
	t.Helper()
	owner, err := tool.New("lookup")
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestReplacementRetainsResourcesUntilCleanupSucceeds(t *testing.T) {
	ctx := context.Background()
	owner := newTool(t)
	old := &resource{}
	next := &resource{}
	first := tool.Revision{
		ID:      "initial",
		Version: 1,
	}
	second := tool.Revision{
		ID:      "replacement",
		Version: 2,
	}
	input := implementation(old)
	if err := owner.Prepare(first, input); err != nil {
		t.Fatal(err)
	}
	input.Definition.Parameters[0] = '!'
	if err := owner.Commit(first); err != nil {
		t.Fatal(err)
	}
	view, err := owner.Definition(1)
	if err != nil || !json.Valid(view.Parameters) {
		t.Fatalf("definition was not detached: %v", err)
	}
	view.Output.Schema[0] = '!'
	view, err = owner.Definition(1)
	if err != nil || !json.Valid(view.Output.Schema) {
		t.Fatalf("returned definition aliases owner: %v", err)
	}
	if err := owner.Prepare(second, implementation(next)); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Definition(1); err == nil || old.calls != 0 {
		t.Fatal("preparation admitted execution or released the current resource")
	}
	if err := owner.Commit(first); err != nil {
		t.Fatal("retrying an earlier completed commit should not change the candidate")
	}
	if err := owner.Commit(second); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("connection close failed")
	old.err = failure
	if err := owner.Retire(ctx, second); !errors.Is(err, failure) {
		t.Fatalf("cleanup failure lost: %v", err)
	}
	if _, err := owner.Definition(2); err == nil {
		t.Fatal("cleanup failure must keep execution blocked")
	}
	old.err = nil
	for range 2 {
		if err := owner.Commit(second); err != nil {
			t.Fatal(err)
		}
		if err := owner.Retire(ctx, second); err != nil {
			t.Fatal(err)
		}
	}
	if old.calls != 2 || next.calls != 0 {
		t.Fatalf("incorrect resource lifetime: old=%d new=%d", old.calls, next.calls)
	}
	if _, err := owner.Definition(1); err == nil {
		t.Fatal("old version remained executable")
	}
	if _, err := owner.Definition(2); err != nil {
		t.Fatal(err)
	}
	if err := owner.Prepare(first, implementation(nil)); err == nil {
		t.Fatal("stale revision accepted")
	}
	if err := owner.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(ctx); err != nil || next.calls != 1 {
		t.Fatalf("close was not idempotent: %v, calls=%d", err, next.calls)
	}
}

func TestFailedAbortCannotCommitPartiallyReleasedCandidate(t *testing.T) {
	owner := newTool(t)
	failure := errors.New("partial release")
	owned := &resource{
		err: failure,
	}
	target := tool.Revision{
		ID:      "aborted",
		Version: 1,
	}
	if err := owner.Prepare(target, implementation(owned)); err != nil {
		t.Fatal(err)
	}
	if err := owner.Abort(context.Background(), target); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := owner.Commit(target); err == nil {
		t.Fatal("partially released candidate was committed")
	}
	owned.err = nil
	if err := owner.Abort(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(context.Background()); err != nil || owned.calls != 2 {
		t.Fatalf("aborted resource released again: %v, calls=%d", err, owned.calls)
	}
}

func TestCloseAttemptsEveryResourceAndRetriesOnlyFailures(t *testing.T) {
	owner := newTool(t)
	first := tool.Revision{
		ID:      "first",
		Version: 1,
	}
	second := tool.Revision{
		ID:      "second",
		Version: 2,
	}
	old := &resource{}
	failure := errors.New("candidate close failed")
	next := &resource{
		err: failure,
	}
	if err := owner.Prepare(first, implementation(old)); err != nil {
		t.Fatal(err)
	}
	if err := owner.Commit(first); err != nil {
		t.Fatal(err)
	}
	if err := owner.Prepare(second, implementation(next)); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if old.calls != 1 || next.calls != 1 {
		t.Fatal("one failure prevented cleanup of another resource")
	}
	if err := owner.Commit(second); err == nil {
		t.Fatal("closing owner accepted commit")
	}
	next.err = nil
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if old.calls != 1 || next.calls != 2 {
		t.Fatal("successful cleanup was repeated")
	}
}

func TestCanceledAbortRetainsCleanupAndRejectsActivation(t *testing.T) {
	owner := newTool(t)
	owned := &resource{}
	target := tool.Revision{
		ID:      "canceled",
		Version: 1,
	}
	if err := owner.Prepare(target, implementation(owned)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := owner.Abort(ctx, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if owned.calls != 0 {
		t.Fatal("started resource cleanup with an already canceled context")
	}
	if err := owner.Commit(target); err == nil {
		t.Fatal("canceled cleanup reactivated the discarded candidate")
	}
	if err := owner.Close(context.Background()); err != nil || owned.calls != 1 {
		t.Fatalf("resource lost after cancellation: %v, calls=%d", err, owned.calls)
	}
}

func TestInvalidPreparationDoesNotTakeResourceOwnership(t *testing.T) {
	owner := newTool(t)
	owned := &resource{}
	input := implementation(owned)
	input.Definition.Output.Schema = json.RawMessage(`{"type":"not-a-json-schema-type"}`)
	if err := owner.Prepare(tool.Revision{
		ID:      "invalid",
		Version: 1,
	}, input); err == nil {
		t.Fatal("invalid output schema accepted")
	}
	if err := owner.Close(context.Background()); err != nil || owned.calls != 0 {
		t.Fatalf("rejected resource was taken from caller: %v, calls=%d", err, owned.calls)
	}
}

func TestCandidateDefinitionRegistersWithoutExposingExecution(t *testing.T) {
	owner := newTool(t)
	target := tool.Revision{
		ID:      "catalog-update",
		Version: 1,
	}
	var directory toolregistry.Registry
	if err := directory.Begin(toolregistry.Update{
		ID:      toolregistry.UpdateID(target.ID),
		AgentID: "conversation",
		Names:   []string{"lookup"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := owner.Prepare(target, implementation(nil)); err != nil {
		t.Fatal(err)
	}
	schema, err := owner.Schema(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Register(toolregistry.UpdateID(target.ID), toolregistry.Candidate{
		Schema:  schema,
		Visible: true,
	}); err != nil {
		t.Fatal(err)
	}
	schema.Parameters[0] = '!'
	if _, err := owner.Definition(1); err == nil {
		t.Fatal("candidate became executable before publication and switching")
	}
	committed, err := directory.Publish(toolregistry.UpdateID(target.ID))
	if err != nil {
		t.Fatal(err)
	}
	if committed.Version != target.Version ||
		len(committed.Definitions.Schemas) != 1 ||
		!json.Valid(committed.Definitions.Schemas[0].Parameters) {
		t.Fatalf("invalid published catalog: %#v", committed)
	}
	if _, err := owner.Definition(committed.Version); err == nil {
		t.Fatal("publication alone switched the tool implementation")
	}
	if err := owner.Commit(target); err != nil {
		t.Fatal(err)
	}
	active, err := owner.Definition(committed.Version)
	if err != nil || !json.Valid(active.Parameters) {
		t.Fatalf("invalid active definition: %v", err)
	}
	if _, err := owner.Schema(target); err == nil {
		t.Fatal("committed implementation remained a candidate")
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardedCandidateCannotRegisterDefinition(t *testing.T) {
	owner := newTool(t)
	target := tool.Revision{
		ID:      "discarded",
		Version: 1,
	}
	owned := &resource{
		err: errors.New("cleanup failure"),
	}
	if err := owner.Prepare(target, implementation(owned)); err != nil {
		t.Fatal(err)
	}
	other := target
	other.ID = "different-update"
	if _, err := owner.Schema(other); err == nil {
		t.Fatal("different update read a candidate")
	}
	if err := owner.Abort(context.Background(), target); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if _, err := owner.Schema(target); err == nil {
		t.Fatal("discarded candidate remained available for registration")
	}
	owned.err = nil
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
