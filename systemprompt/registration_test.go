package systemprompt

import (
	"context"
	"errors"
	"testing"
)

func TestRegistrationRemovalIdentity(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		add  func(*PromptLayer) (RegistrationID, error)
	}{
		{
			name: "section",
			add: func(owner *PromptLayer) (RegistrationID, error) {
				return owner.AddSection(ctx, PromptSection{
					Name: "entry",
					Text: StaticText("instruction"),
				})
			},
		},
		{
			name: "context",
			add: func(owner *PromptLayer) (RegistrationID, error) {
				return owner.AddContext(ctx, PromptContext{
					Name: "entry",
					Text: StaticText("context"),
				})
			},
		},
		{
			name: "variable",
			add: func(owner *PromptLayer) (RegistrationID, error) {
				return owner.AddVariable(ctx, "entry", registrationVariable{})
			},
		},
		{
			name: "suppressor",
			add: func(owner *PromptLayer) (RegistrationID, error) {
				return owner.AddRuntimeContextSuppressor(ctx, "entry")
			},
		},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			owner := DeriveLayer(Snapshot{})
			other := DeriveLayer(Snapshot{})
			original, err := scenario.add(owner)
			if err != nil {
				t.Fatal(err)
			}
			foreign, err := scenario.add(other)
			if err != nil {
				t.Fatal(err)
			}
			if removed, err := owner.Remove(ctx, foreign); removed || err != nil {
				t.Fatalf("foreign removal = %v, %v", removed, err)
			}
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if removed, err := owner.Remove(canceled, original); removed || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled removal = %v, %v", removed, err)
			}
			if removed, err := owner.Remove(ctx, original); !removed || err != nil {
				t.Fatalf("removal = %v, %v", removed, err)
			}
			replacement, err := scenario.add(owner)
			if err != nil {
				t.Fatal(err)
			}
			if replacement == original {
				t.Fatal("registration ID reused")
			}
			if removed, err := owner.Remove(ctx, original); removed || err != nil {
				t.Fatalf("stale removal = %v, %v", removed, err)
			}
			if err := owner.Close(ctx); err != nil {
				t.Fatal(err)
			}
			latest, err := scenario.add(owner)
			if err != nil {
				t.Fatal(err)
			}
			if removed, err := owner.Remove(ctx, replacement); removed || err != nil {
				t.Fatalf("previous lifetime removal = %v, %v", removed, err)
			}
			if removed, err := owner.Remove(ctx, latest); !removed || err != nil {
				t.Fatalf("latest removal = %v, %v", removed, err)
			}
		})
	}
}

type registrationVariable struct{}

func (registrationVariable) ResolveVariable(context.Context, AssembleContext) (VariableValue, error) {
	return VariableValue{
		Value:   "value",
		Defined: true,
	}, nil
}
