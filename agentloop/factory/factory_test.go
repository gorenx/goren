package factory_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorenx/goren/agentloop"
	agentloopfactory "github.com/gorenx/goren/agentloop/factory"
)

func TestFactoryCreatesAgentLoopPlugin(t *testing.T) {
	t.Parallel()
	builder := agentloopfactory.New(agentloop.RuntimeOptions{})
	created, err := builder.Create(
		context.Background(),
		json.RawMessage(`{
			"maxParallelToolCalls": 3,
			"agents": [
				{"id":"fresh","sessionId":"fresh-session","provider":"p","model":"m","cwd":"/work"},
				{"id":"resume","resumeSessionId":"stored-session"}
			]
		}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, matches := created.(*agentloop.Plugin); !matches {
		t.Fatalf("created Plugin type = %T", created)
	}
	if created.Manifest().Name != agentloop.PluginName {
		t.Fatalf("Plugin name = %q", created.Manifest().Name)
	}
}

func TestFactoryAppliesRuntimeDefaults(t *testing.T) {
	t.Parallel()
	builder := agentloopfactory.New(agentloop.RuntimeOptions{})
	created, err := builder.Create(
		context.Background(),
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, matches := created.(*agentloop.Plugin); !matches {
		t.Fatalf("created Plugin type = %T", created)
	}
}

func TestFactoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	builder := agentloopfactory.New(agentloop.RuntimeOptions{})
	testCases := []struct {
		label       string
		input       string
		wantMessage string
	}{
		{
			label:       "non object",
			input:       `[]`,
			wantMessage: "JSON object",
		},
		{
			label:       "unknown root field",
			input:       `{"unknown":true}`,
			wantMessage: "unknown field",
		},
		{
			label:       "unknown Agent field",
			input:       `{"agents":[{"id":"a","unknown":true}]}`,
			wantMessage: "unknown field",
		},
		{
			label:       "null cap",
			input:       `{"maxParallelToolCalls":null}`,
			wantMessage: "positive integer",
		},
		{
			label:       "invalid cap",
			input:       `{"maxParallelToolCalls":0}`,
			wantMessage: "positive integer",
		},
		{
			label:       "untrimmed id",
			input:       `{"agents":[{"id":" a"}]}`,
			wantMessage: "non-empty and trimmed",
		},
		{
			label:       "conflicting identity",
			input:       `{"agents":[{"id":"a","sessionId":"s","resumeSessionId":"r"}]}`,
			wantMessage: "mutually exclusive",
		},
		{
			label:       "duplicate identity",
			input:       `{"agents":[{"id":"a","sessionId":"s"},{"id":"b","resumeSessionId":"s"}]}`,
			wantMessage: "duplicate exact Session identity",
		},
		{
			label:       "null Agent string",
			input:       `{"agents":[{"id":"a","provider":null}]}`,
			wantMessage: "must be a string",
		},
		{
			label:       "invalid token limit",
			input:       `{"agents":[{"id":"a","maxTokens":0}]}`,
			wantMessage: "positive safe integer",
		},
		{
			label:       "relative working directory",
			input:       `{"agents":[{"id":"a","cwd":"relative"}]}`,
			wantMessage: "absolute path",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.label, func(t *testing.T) {
			t.Parallel()
			_, err := builder.Create(
				context.Background(),
				json.RawMessage(testCase.input),
			)
			if err == nil || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Fatalf("Create error = %v, want %q", err, testCase.wantMessage)
			}
		})
	}
}
