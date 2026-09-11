package server

import (
	"context"
	"errors"
	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/llm/deepseek"
	"github.com/gorenx/goren/settings/local"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/tools"
	catalogactor "github.com/gorenx/goren/tools/registry/actor"
	goakt "github.com/tochemey/goakt/v4/actor"
	"slices"
	"time"

	imageapi "github.com/gorenx/goren/apiproxy/session/image"
	"github.com/gorenx/goren/llm/deepseek/anonymoususerid"
	"github.com/gorenx/goren/llm/modelconfig"
	"github.com/gorenx/goren/mcpclient"
	usersettings "github.com/gorenx/goren/settings"
	settingsactor "github.com/gorenx/goren/settings/actor"
	"github.com/gorenx/goren/systemprompt"
	promptactor "github.com/gorenx/goren/systemprompt/actor"
	toolregistry "github.com/gorenx/goren/tools/registry"
	"github.com/gorenx/goren/tools/tool"
)

// Config contains process paths and owner-defined configuration, never secrets.
type Config struct {
	Agents     map[string]usersettings.Values
	Companions map[string]companion.Config
	// Attachments is a borrowed image capability shared by admission and reads.
	// Nil creates an owned original-image Store beside SessionDatabase.
	// Composition closes an injected capability after Server.Close.
	Attachments       imageapi.ImageStore
	Address           string
	Version           string
	WorkingDirectory  string
	SessionDatabase   string
	WorkspaceDatabase string
	QueryDatabase     string
	CacheDatabase     string
	CredentialsPath   string
	SettingsDatabase  string
	Models            modelconfig.Definition
	Timeout           time.Duration
	Provider          deepseek.Config
	Prompt            systemprompt.Config
	Tools             toolregistry.Catalog
	Providers         tool.Providers
	MCP               []mcpclient.Config
}

// Inspect checks the persisted user target without creating or migrating a
// database. Startup reloads it before constructing any consuming capability.
func (settings Config) Inspect(ctx context.Context, platform anonymoususerid.Environment) error {
	if ctx == nil {
		return errors.New("server: configuration context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	validated, err := settings.Validate(platform)
	if err != nil {
		return err
	}
	target, err := settingsactor.Inspect(ctx, validated.SettingsDatabase, validated.userBaseline())
	if err != nil {
		return err
	}
	_, err = validated.withUserSettings(target).Validate(platform)
	return err
}

func (settings Config) userBaseline() usersettings.Directory {
	provider := usersettings.ProviderConfig{
		APIKeyEnv:           settings.Provider.APIKeyEnv,
		BaseURL:             settings.Provider.BaseURL,
		StreamIdleTimeoutMS: settings.Provider.StreamIdleTimeoutMS,
	}
	if policy := settings.Provider.RetryPolicy; policy != nil {
		provider.RetryPolicy = &usersettings.RetryConfig{
			Mode:       &policy.Mode,
			MaxRetries: policy.MaxRetries,
			Backoff:    policy.Backoff,
		}
		if policy.RetryableCodes != nil {
			provider.RetryPolicy.RetryableCodes = &policy.RetryableCodes
		}
	}
	prompt := usersettings.PromptConfig{
		IncludeHarnessIdentity: settings.Prompt.IncludeHarnessIdentity,
		IncludeRuntimeContext:  settings.Prompt.IncludeRuntimeContext,
		Persona:                &settings.Prompt.Persona,
	}
	if settings.Prompt.ToolOrder != nil {
		prompt.ToolOrder = &settings.Prompt.ToolOrder
	}
	httpDefinitions := make(map[string]usersettings.MCPConfig)
	for _, entry := range settings.MCP {
		if entry.Transport == "streamable-http" {
			httpDefinitions[entry.ServerName] = usersettings.MCPConfig{
				Enabled:    true,
				Connection: entry,
			}
		}
	}
	toolGroups := make(map[string]tools.Config)
	catalog := defaultTools(settings.Tools)
	for _, entry := range catalog.Schemas {
		if entry.Name == approval.ServiceName {
			continue
		}
		policy := catalog.Policies[entry.Name]
		toolGroups[entry.Name] = tools.Config{
			Enabled: policy.Enabled,
			Visible: policy.Visible,
		}.Clone()
	}
	return usersettings.Directory{
		Agents:     settings.Agents,
		Companions: settings.Companions,
		Values: usersettings.Values{
			Providers: map[string]usersettings.ProviderConfig{
				"deepseek": provider.Clone(),
			},
			Prompts: map[string]usersettings.PromptConfig{
				"default": prompt.Clone(),
			},
			MCP:   httpDefinitions,
			Tools: toolGroups,
		},
	}
}

func (settings Config) withUserSettings(target usersettings.Directory) Config {
	selected := target.Clone()
	settings.Agents = selected.Agents
	settings.Companions = selected.Companions
	settings.Provider.APIKeyEnv = selected.Providers["deepseek"].APIKeyEnv
	settings.Provider.BaseURL = selected.Providers["deepseek"].BaseURL
	// Local source precedence has already been resolved into the baseline. An
	// omitted endpoint in a complete user group uses the capability default.
	if settings.Provider.BaseURL == nil {
		endpoint := deepseek.PublicBaseURL
		settings.Provider.BaseURL = &endpoint
	}
	settings.Provider.StreamIdleTimeoutMS = selected.Providers["deepseek"].StreamIdleTimeoutMS
	settings.Provider.RetryPolicy = nil
	if selected.Providers["deepseek"].RetryPolicy != nil {
		policy := selected.Providers["deepseek"].RetryPolicy.Policy()
		settings.Provider.RetryPolicy = &policy
	}
	settings.Prompt = selected.Prompts["default"].Prompt()
	definitions := slices.DeleteFunc(slices.Clone(settings.MCP), func(entry mcpclient.Config) bool {
		return entry.Transport == "streamable-http"
	})
	definitions = append(definitions, selected.MCPConnections()...)
	settings.Tools = settings.Tools.Clone()
	if settings.Tools.Policies == nil {
		settings.Tools.Policies = make(map[string]toolregistry.Policy)
	}
	for name, configuration := range selected.Tools {
		enabled, visible := configuration.Resolve()
		policy := settings.Tools.Policies[name]
		policy.Enabled = &enabled
		policy.Visible = &visible
		settings.Tools.Policies[name] = policy
	}
	settings.MCP = definitions
	return settings
}

func (owner *Server) openSettings(ctx context.Context) error {
	if err := owner.settings.Inspect(ctx, owner.platform); err != nil {
		return err
	}
	installed, err := settingsactor.Open(ctx, owner.settings.SettingsDatabase, owner.settings.userBaseline())
	if err != nil {
		return err
	}
	owner.preferences = installed
	selected, err := owner.settings.withUserSettings(installed.Target()).Validate(owner.platform)
	if err != nil {
		return err
	}
	owner.settings = selected
	return nil
}

// ConfigFromLocal maps decoded deployment values to capability-owned inputs.
// It reads explicitly referenced MCP files through the local configuration
// capability, but creates no runtime resources and performs no network calls.
func ConfigFromLocal(input local.Config, directory string, version string) (Config, error) {
	if input.Storage.SessionDatabase == nil ||
		input.Storage.WorkspaceDatabase == nil ||
		input.Storage.QueryDatabase == nil ||
		input.Storage.CacheDatabase == nil ||
		input.Storage.CredentialsPath == nil ||
		input.Settings.Database == nil {
		return Config{}, errors.New("server: local storage paths have not been resolved")
	}
	connections, err := input.Connections()
	if err != nil {
		return Config{}, err
	}
	provider := input.LLM.DeepSeek.Clone()
	output := Config{
		Agents: (usersettings.Directory{
			Agents: input.Agents,
		}).Clone().Agents,
		Companions: (usersettings.Directory{
			Companions: input.Companions,
		}).Clone().Companions,
		Version:           version,
		WorkingDirectory:  directory,
		SessionDatabase:   *input.Storage.SessionDatabase,
		WorkspaceDatabase: *input.Storage.WorkspaceDatabase,
		QueryDatabase:     *input.Storage.QueryDatabase,
		CacheDatabase:     *input.Storage.CacheDatabase,
		CredentialsPath:   *input.Storage.CredentialsPath,
		SettingsDatabase:  *input.Settings.Database,
		Models:            DefaultModelConfig(),
		Provider: deepseek.Config{
			APIKeyEnv:           provider.APIKeyEnv,
			BaseURL:             provider.BaseURL,
			StreamIdleTimeoutMS: provider.StreamIdleTimeoutMS,
		},
		Prompt: input.Prompt.Prompt(),
		MCP:    connections,
	}
	if provider.RetryPolicy != nil {
		policy := provider.RetryPolicy.Policy()
		output.Provider.RetryPolicy = &policy
	}
	if input.Server.Listen != nil {
		output.Address = *input.Server.Listen
	}
	if input.Server.RequestTimeoutMS != nil {
		output.Timeout = time.Duration(*input.Server.RequestTimeoutMS) * time.Millisecond
	}
	selected := input.LLM.Default
	if selected.Provider != nil {
		output.Models.Default.Provider = *selected.Provider
	}
	if selected.Model != nil {
		output.Models.Default.Model = *selected.Model
	}
	if selected.ReasoningEffort != nil {
		output.Models.Default.ReasoningEffort = *selected.ReasoningEffort
	}
	parameters := llm.CloneCallConfig(llm.CallConfig{
		Temperature: selected.Temperature,
		MaxTokens:   selected.MaxTokens,
	})
	output.Models.Default.Temperature = parameters.Temperature
	output.Models.Default.MaxTokens = parameters.MaxTokens
	if selected.Stop != nil {
		output.Models.Default.Stop = slices.Clone(*selected.Stop)
	}
	catalog := defaultTools(output.Tools)
	for name, configuration := range input.Tools {
		if name == approval.ServiceName || !slices.ContainsFunc(catalog.Schemas, func(entry llm.ToolSchema) bool { return entry.Name == name }) {
			return Config{}, usersettings.ErrInvalid
		}
		enabled, visible := configuration.Resolve()
		policy := catalog.Policies[name]
		policy.Enabled = &enabled
		policy.Visible = &visible
		catalog.Policies[name] = policy
	}
	output.Tools = catalog
	connection, err := deepseek.ResolveOptions(output.Provider, nil)
	if err != nil {
		return Config{}, errors.New("server: invalid local provider configuration")
	}
	output.Provider.BaseURL = &connection.BaseURL
	output.Provider.APIKeyEnv = &connection.APIKeyEnv
	return output, nil
}

// promptSettings maps exposed configuration names to the installed Prompt owner.
// It borrows the Client; Server owns the registry lifetime.
type promptSettings struct {
	registry *promptactor.Client
}

func (endpoint promptSettings) ApplyPrompt(ctx context.Context, name string, configuration systemprompt.Config) error {
	if name != "default" {
		return usersettings.ErrInvalid
	}
	return endpoint.registry.ApplyBaseline(ctx, configuration)
}

// toolSettings maps exposed names to the existing Registry's configuration use case.
// Framework identity and source names are borrowed; Server owns their lifetimes.
type toolSettings struct {
	system   goakt.ActorSystem
	registry *goakt.GrainIdentity
	timeout  time.Duration
	names    map[string]struct{}
}

func (endpoint toolSettings) ValidateTool(ctx context.Context, name string) error {
	if _, exposed := endpoint.names[name]; !exposed {
		return usersettings.ErrInvalid
	}
	payload, err := endpoint.system.AskGrain(ctx, endpoint.registry, catalogactor.ReadConfiguration{}, endpoint.timeout)
	if err != nil {
		return err
	}
	reply, valid := payload.(catalogactor.Configuration)
	if !valid {
		return usersettings.ErrApplication
	}
	if !slices.ContainsFunc(reply.Value.Global.Schemas, func(entry llm.ToolSchema) bool { return entry.Name == name }) {
		return usersettings.ErrInvalid
	}
	return nil
}

func (endpoint toolSettings) ApplyTool(ctx context.Context, name string, configuration tools.Config) error {
	if _, exposed := endpoint.names[name]; !exposed {
		return usersettings.ErrInvalid
	}
	payload, err := endpoint.system.AskGrain(ctx, endpoint.registry, catalogactor.ConfigureTool{
		Name:   name,
		Config: configuration.Clone(),
	}, endpoint.timeout)
	if err != nil {
		return err
	}
	reply, valid := payload.(catalogactor.Completion)
	if !valid {
		return usersettings.ErrApplication
	}
	return reply.Err
}
