package agentloop

import (
	"context"
	"errors"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/systemprompt"
)

type agentVariables struct {
	plugin.Base
	entries []namedAgentVariable
	handles []*systemprompt.PromptHandle
}

type namedAgentVariable struct {
	name     string
	provider staticAgentVariable
}

type staticAgentVariable struct {
	value string
}

func newAgentVariables(
	loopOptions agent.Options,
	headerSnapshot session.Header,
) *agentVariables {
	workingDirectory := ""
	if headerSnapshot.CWD != nil {
		workingDirectory = *headerSnapshot.CWD
	}
	return &agentVariables{
		entries: []namedAgentVariable{
			{
				name: "provider",
				provider: staticAgentVariable{
					value: loopOptions.Provider,
				},
			},
			{
				name: "model",
				provider: staticAgentVariable{
					value: loopOptions.Model,
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

func (*agentVariables) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name: PluginName + "/variables",
		Requires: []plugin.ServiceType{
			plugin.ServiceOf[systemprompt.PromptRegistry](),
		},
	}
}

func (variablesOwner *agentVariables) Apply(
	requestContext context.Context,
) error {
	if err := requestContext.Err(); err != nil {
		return err
	}
	prompts, err := plugin.Require[systemprompt.PromptRegistry](variablesOwner)
	if err != nil {
		return err
	}
	for entryIndex := range variablesOwner.entries {
		entry := variablesOwner.entries[entryIndex]
		promptHandle, addErr := prompts.AddVariable(
			requestContext,
			entry.name,
			entry.provider,
		)
		if addErr != nil {
			return errors.Join(
				addErr,
				variablesOwner.removeInstalled(requestContext),
			)
		}
		variablesOwner.handles = append(
			variablesOwner.handles,
			promptHandle,
		)
	}
	return requestContext.Err()
}

func (variablesOwner *agentVariables) Dispose(
	closeContext context.Context,
) error {
	disposeErr := variablesOwner.removeInstalled(closeContext)
	return disposeErr
}

func (variablesOwner *agentVariables) removeInstalled(
	closeContext context.Context,
) error {
	var removeErr error
	for len(variablesOwner.handles) > 0 {
		lastIndex := len(variablesOwner.handles) - 1
		promptHandle := variablesOwner.handles[lastIndex]
		variablesOwner.handles = variablesOwner.handles[:lastIndex]
		removeErr = errors.Join(
			removeErr,
			promptHandle.Unregister(closeContext),
		)
	}
	return removeErr
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
