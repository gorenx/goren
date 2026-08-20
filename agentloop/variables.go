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
	entries   []namedAgentVariable
	prompts   systemprompt.PromptRegistry
	installed int
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
	variablesOwner.prompts = prompts
	for entryIndex := range variablesOwner.entries {
		entry := variablesOwner.entries[entryIndex]
		if err = prompts.AddVariable(
			requestContext,
			entry.name,
			entry.provider,
		); err != nil {
			return errors.Join(err, variablesOwner.removeInstalled(requestContext))
		}
		variablesOwner.installed++
	}
	return requestContext.Err()
}

func (variablesOwner *agentVariables) Dispose(
	closeContext context.Context,
) error {
	disposeErr := variablesOwner.removeInstalled(closeContext)
	variablesOwner.prompts = nil
	return disposeErr
}

func (variablesOwner *agentVariables) removeInstalled(
	closeContext context.Context,
) error {
	if variablesOwner.prompts == nil {
		variablesOwner.installed = 0
		return nil
	}
	var removeErr error
	for variablesOwner.installed > 0 {
		variablesOwner.installed--
		entry := variablesOwner.entries[variablesOwner.installed]
		removeErr = errors.Join(
			removeErr,
			variablesOwner.prompts.RemoveVariable(closeContext, entry.name),
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
