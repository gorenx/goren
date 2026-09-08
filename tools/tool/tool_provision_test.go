package tool_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gorenx/goren/tools/tool"
)

type provider struct {
	input tool.Implementation
	err   error
	open  int
	stop  context.CancelFunc
}

func (source *provider) Open(context.Context) (tool.Implementation, error) {
	source.open++
	if source.stop != nil {
		source.stop()
	}
	return source.input, source.err
}

func TestProvisionCleansPartialConstructionAndRetainsFailedCleanup(t *testing.T) {
	for _, failCleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleanup succeeds", true: "cleanup fails"}[failCleanup], func(t *testing.T) {
			owner := newTool(t)
			opening := errors.New("opening failed")
			cleanup := errors.New("cleanup failed")
			owned := &resource{}
			if failCleanup {
				owned.err = cleanup
			}
			source := &provider{
				input: implementation(owned),
				err:   opening,
			}
			target := tool.Revision{
				ID:      "failed",
				Version: 1,
			}
			failure := owner.Provision(context.Background(), target, source)
			if !errors.Is(failure, opening) || errors.Is(failure, cleanup) != failCleanup {
				t.Fatalf("construction or cleanup error lost: %v", failure)
			}
			if owned.calls != 1 || source.open != 1 {
				t.Fatal("partial construction was not cleaned up once")
			}
			if _, err := owner.Schema(target); err == nil {
				t.Fatal("failed construction exposed a candidate schema")
			}
			if err := owner.Commit(target); err == nil {
				t.Fatal("failed construction was committed")
			}
			owned.err = nil
			if err := owner.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := 1
			if failCleanup {
				want = 2
			}
			if owned.calls != want {
				t.Fatalf("cleanup ownership lost: calls=%d want=%d", owned.calls, want)
			}
		})
	}
}

func TestProvisionCancellationCleansResourcesReturnedByProvider(t *testing.T) {
	owner := newTool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owned := &resource{}
	source := &provider{
		input: implementation(owned),
		stop:  cancel,
	}
	target := tool.Revision{
		ID:      "cancelled",
		Version: 1,
	}
	if err := owner.Provision(ctx, target, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if owned.calls != 1 {
		t.Fatal("cancelled preparation skipped resource cleanup")
	}
	if _, err := owner.Schema(target); err == nil {
		t.Fatal("cancelled preparation registered a candidate")
	}
}

func TestProvisionRejectsInvalidDefinitionAndDuplicateBeforeOpening(t *testing.T) {
	owner := newTool(t)
	owned := &resource{}
	source := &provider{
		input: implementation(owned),
	}
	source.input.Definition.Name = "different"
	target := tool.Revision{
		ID:      "update",
		Version: 1,
	}
	if err := owner.Provision(context.Background(), target, source); err == nil || owned.calls != 1 {
		t.Fatalf("invalid definition resource was not cleaned: %v", err)
	}
	source.input = implementation(nil)
	if err := owner.Provision(context.Background(), target, source); err != nil {
		t.Fatal(err)
	}
	if err := owner.Provision(context.Background(), target, source); err == nil || source.open != 2 {
		t.Fatal("duplicate preparation opened another implementation")
	}
	if err := owner.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
