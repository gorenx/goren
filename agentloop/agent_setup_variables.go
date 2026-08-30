package agentloop

import (
	"context"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

// agentVariablesSetup adds immutable Agent configuration to its prompt layer.
type agentVariablesSetup struct {
	variables []namedAgentVariable
}

func newAgentVariablesSetup(
	options agent.Options,
	header session.Header,
) agentVariablesSetup {
	workingDirectory := ""
	if header.CWD != nil {
		workingDirectory = *header.CWD
	}
	return agentVariablesSetup{
		variables: []namedAgentVariable{
			{
				name: "provider",
				provider: staticAgentVariable{
					value: options.Provider,
				},
			},
			{
				name: "model",
				provider: staticAgentVariable{
					value: options.Model,
				},
			},
			{
				name: "cwd",
				provider: staticAgentVariable{
					value: workingDirectory,
				},
			},
		},
	}
}

func (setup agentVariablesSetup) Apply(
	requestContext context.Context,
	_ agent.Agent,
	editor agent.ScopeEditor,
) error {
	for _, entry := range setup.variables {
		if err := editor.AddPromptVariable(
			requestContext,
			entry.name,
			entry.provider,
		); err != nil {
			return err
		}
	}
	return nil
}

type namedAgentVariable struct {
	name     string
	provider staticAgentVariable
}

type staticAgentVariable struct {
	value string
}

func (providerValue staticAgentVariable) ResolveVariable(
	context.Context,
	systemprompt.AssembleContext,
) (systemprompt.VariableValue, error) {
	return systemprompt.VariableValue{
		Value:   providerValue.value,
		Defined: providerValue.value != "",
	}, nil
}

var _ agent.Setup = agentVariablesSetup{}
