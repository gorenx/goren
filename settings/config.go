// Package settings owns user configuration overrides, conditional persistence,
// and application status. Runtime resources remain with their capability owners.
package settings

import (
	"errors"
	"math"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/credentials"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/subagent/companion"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// ProviderConfig contains optional connection settings, never literal credentials.
// Omitted members use capability defaults; request model selection belongs to Session.
type ProviderConfig struct {
	APIKeyEnv           *string      `json:"apiKeyEnv,omitempty"`
	BaseURL             *string      `json:"baseURL,omitempty"`
	StreamIdleTimeoutMS *float64     `json:"streamIdleTimeoutMs,omitempty"`
	RetryPolicy         *RetryConfig `json:"retryPolicy,omitempty"`
}

// RetryConfig contains optional inputs for one complete retry policy.
type RetryConfig struct {
	Mode           *llm.RetryMode     `json:"mode,omitempty"`
	MaxRetries     *int64             `json:"maxRetries,omitempty"`
	RetryableCodes *[]string          `json:"retryableCodes,omitempty"`
	Backoff        *llm.BackoffConfig `json:"backoff,omitempty"`
}

// Policy maps optional settings to the capability-owned retry input.
func (input RetryConfig) Policy() llm.RetryPolicyConfig {
	output := llm.RetryPolicyConfig{
		Mode:       llm.RetryNormal,
		MaxRetries: copyPointer(input.MaxRetries),
	}
	if input.Mode != nil {
		output.Mode = *input.Mode
	}
	if input.RetryableCodes != nil {
		output.RetryableCodes = slices.Clone(*input.RetryableCodes)
		if output.RetryableCodes == nil {
			output.RetryableCodes = []string{}
		}
	}
	if input.Backoff != nil {
		output.Backoff = &llm.BackoffConfig{
			InitialDelayMS: copyPointer(input.Backoff.InitialDelayMS),
			MaxDelayMS:     copyPointer(input.Backoff.MaxDelayMS),
			JitterRatio:    copyPointer(input.Backoff.JitterRatio),
		}
	}
	return output
}

// PromptConfig preserves omission independently of false and empty text.
type PromptConfig struct {
	IncludeHarnessIdentity *bool     `json:"includeHarnessIdentity,omitempty"`
	IncludeRuntimeContext  *bool     `json:"includeRuntimeContext,omitempty"`
	Persona                *string   `json:"persona,omitempty"`
	ToolOrder              *[]string `json:"toolOrder,omitempty"`
}

// MCPConfig is one complete named connection. Disabled configurations retain
// their definition; removing an override restores the entire inherited group.
type MCPConfig struct {
	Enabled    bool             `json:"enabled"`
	Connection mcpclient.Config `json:"connection"`
}

// Values composes capability-owned configuration for the default Agent or a
// named Agent. It contains no directory, lifecycle policy, or Session identity.
type Values struct {
	Model     agent.Options             `json:"model,omitempty"`
	Providers map[string]ProviderConfig `json:"providers,omitempty"`
	Prompts   map[string]PromptConfig   `json:"prompts,omitempty"`
	MCP       map[string]MCPConfig      `json:"mcp,omitempty"`
	Tools     map[string]tools.Config   `json:"tools,omitempty"`
}

// Directory owns names and creation policies separately from capability values.
// Embedding Values makes the process defaults use exactly the same value types.
type Directory struct {
	Values
	Agents     map[string]Values
	Companions map[string]companion.Config
}

// Clone returns detached capability values.
func (input Values) Clone() Values {
	return Values{
		Model:     input.Model.Clone(),
		Providers: cloneGroups(input.Providers),
		Prompts:   cloneGroups(input.Prompts),
		MCP:       cloneGroups(input.MCP),
		Tools:     cloneGroups(input.Tools),
	}
}

// Clone returns a detached directory and all named configurations.
func (input Directory) Clone() Directory {
	return Directory{
		Values:     input.Values.Clone(),
		Agents:     cloneGroups(input.Agents),
		Companions: cloneGroups(input.Companions),
	}
}

// Clone returns a detached connection configuration.
func (input ProviderConfig) Clone() ProviderConfig {
	return ProviderConfig{
		APIKeyEnv:           copyPointer(input.APIKeyEnv),
		BaseURL:             copyPointer(input.BaseURL),
		StreamIdleTimeoutMS: copyPointer(input.StreamIdleTimeoutMS),
		RetryPolicy:         copyRetryPolicy(input.RetryPolicy),
	}
}

// Clone returns detached prompt fields, retaining an explicitly empty tool list.
func (input PromptConfig) Clone() PromptConfig {
	return PromptConfig{
		IncludeHarnessIdentity: copyPointer(input.IncludeHarnessIdentity),
		IncludeRuntimeContext:  copyPointer(input.IncludeRuntimeContext),
		Persona:                copyPointer(input.Persona),
		ToolOrder:              copyList(input.ToolOrder),
	}
}

// Clone detaches the complete connection definition.
func (input MCPConfig) Clone() MCPConfig {
	input.Connection.Args = slices.Clone(input.Connection.Args)
	return input
}

// Validate checks a complete group against its external identity, even if disabled.
func (input MCPConfig) Validate(name string) error {
	if name == "" ||
		strings.TrimSpace(name) != name ||
		input.Connection.ServerName != name ||
		input.Connection.Transport != "streamable-http" {
		return ErrInvalid
	}
	if _, err := input.Connection.Validate(); err != nil {
		return ErrInvalid
	}
	return nil
}

func cloneGroups[T interface{ Clone() T }](input map[string]T) map[string]T {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]T, len(input))
	for name, entry := range input {
		output[name] = entry.Clone()
	}
	return output
}

// MCPConnections returns detached enabled definitions in stable name order.
func (input Values) MCPConnections() []mcpclient.Config {
	names := make([]string, 0, len(input.MCP))
	for name, entry := range input.MCP {
		if entry.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	output := make([]mcpclient.Config, 0, len(names))
	for _, name := range names {
		output = append(output, input.MCP[name].Clone().Connection)
	}
	return output
}

// Prompt returns the capability's input value without assigning a new owner.
func (input PromptConfig) Prompt() systemprompt.Config {
	output := systemprompt.Config{
		IncludeHarnessIdentity: copyPointer(input.IncludeHarnessIdentity),
		IncludeRuntimeContext:  copyPointer(input.IncludeRuntimeContext),
	}
	if input.Persona != nil {
		output.Persona = *input.Persona
	}
	if input.ToolOrder != nil {
		output.ToolOrder = slices.Clone(*input.ToolOrder)
		if output.ToolOrder == nil {
			output.ToolOrder = []string{}
		}
	}
	return output
}

// Validate checks complete Provider input against capability defaults.
func (input ProviderConfig) Validate() error {
	if input.APIKeyEnv != nil {
		if credentials.ValidateRef(*input.APIKeyEnv) != nil {
			return errors.New("settings: invalid credential reference")
		}
	}
	if input.BaseURL != nil {
		endpoint, err := url.Parse(*input.BaseURL)
		if err != nil {
			return errors.New("settings: invalid provider endpoint")
		}
		if endpoint.Hostname() == "" ||
			(endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
			endpoint.User != nil ||
			endpoint.RawQuery != "" ||
			endpoint.ForceQuery ||
			endpoint.Fragment != "" {
			return errors.New("settings: invalid provider endpoint")
		}
	}
	if input.StreamIdleTimeoutMS != nil {
		milliseconds := *input.StreamIdleTimeoutMS
		if math.IsNaN(milliseconds) ||
			math.IsInf(milliseconds, 0) ||
			milliseconds <= 0 ||
			milliseconds > llm.MaxTimerDelayMS {
			return errors.New("settings: invalid stream idle timeout")
		}
	}
	if input.RetryPolicy != nil {
		policy := input.RetryPolicy.Policy()
		if _, err := llm.ResolveRetryPolicy(&policy, "retryPolicy"); err != nil {
			return errors.New("settings: invalid retry policy")
		}
	}
	return nil
}

// Validate checks complete Prompt input against capability defaults.
func (input PromptConfig) Validate() error {
	if _, err := systemprompt.ValidateConfig(input.Prompt()); err != nil {
		return ErrInvalid
	}
	return nil
}

// Validate checks every named group's complete configuration.
func (input Values) Validate() error {
	if err := input.Model.Validate(); err != nil {
		return err
	}
	for name, entry := range input.Providers {
		if !(Group{
			Section: ProviderSection,
			Name:    name,
		}).Valid() {
			return ErrInvalid
		}
		if err := entry.Validate(); err != nil {
			return err
		}
	}
	for name, entry := range input.Prompts {
		if !(Group{
			Section: PromptSection,
			Name:    name,
		}).Valid() {
			return ErrInvalid
		}
		if err := entry.Validate(); err != nil {
			return err
		}
	}
	for name := range input.Tools {
		if !(Group{
			Section: ToolSection,
			Name:    name,
		}).Valid() {
			return ErrInvalid
		}
	}
	for name, entry := range input.MCP {
		if !(Group{
			Section: MCPSection,
			Name:    name,
		}).Valid() || entry.Connection.ServerName != name {
			return ErrInvalid
		}
		if _, err := entry.Connection.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate checks named configurations and creation policy before persistence.
func (input Directory) Validate() error {
	for name, entry := range input.MCP {
		if err := entry.Validate(name); err != nil {
			return err
		}
	}
	if err := input.Values.Validate(); err != nil {
		return err
	}
	for name, entry := range input.Agents {
		if !(Group{
			Section: AgentSection,
			Name:    name,
		}).Valid() || entry.Validate() != nil {
			return ErrInvalid
		}
	}
	for name, entry := range input.Companions {
		if companion.ValidateName(name) != nil || entry.Validate() != nil {
			return ErrInvalid
		}
	}
	return nil
}

// Has reports whether an explicit complete group is present.
func (input Directory) Has(identifier Group) bool {
	switch identifier.Section {
	case AgentSection:
		_, found := input.Agents[identifier.Name]
		return found
	case CompanionSection:
		_, found := input.Companions[identifier.Name]
		return found
	case ProviderSection:
		_, found := input.Providers[identifier.Name]
		return found
	case PromptSection:
		_, found := input.Prompts[identifier.Name]
		return found
	case ToolSection:
		_, found := input.Tools[identifier.Name]
		return found
	case MCPSection:
		_, found := input.MCP[identifier.Name]
		return found
	default:
		return false
	}
}

// Resolve replaces complete groups over the local baseline. Provider and Prompt
// names must be installed by composition; MCP permits new connection names.
func Resolve(baseline Directory, overrides Directory) (Directory, error) {
	if err := baseline.Validate(); err != nil {
		return Directory{}, err
	}
	if err := overrides.Validate(); err != nil {
		return Directory{}, err
	}
	output := baseline.Clone()
	for name, entry := range overrides.Agents {
		if output.Agents == nil {
			output.Agents = make(map[string]Values)
		}
		output.Agents[name] = entry.Clone()
	}
	for name, entry := range overrides.Companions {
		if output.Companions == nil {
			output.Companions = make(map[string]companion.Config)
		}
		output.Companions[name] = entry.Clone()
	}
	for name, entry := range overrides.Providers {
		if _, installed := baseline.Providers[name]; !installed {
			return Directory{}, ErrInvalid
		}
		output.Providers[name] = entry.Clone()
	}
	for name, entry := range overrides.Prompts {
		if _, installed := baseline.Prompts[name]; !installed {
			return Directory{}, ErrInvalid
		}
		output.Prompts[name] = entry.Clone()
	}
	for name, entry := range overrides.Tools {
		if _, installed := baseline.Tools[name]; !installed {
			return Directory{}, ErrInvalid
		}
		output.Tools[name] = entry.Clone()
	}
	for name, entry := range overrides.MCP {
		if output.MCP == nil {
			output.MCP = make(map[string]MCPConfig)
		}
		output.MCP[name] = entry.Clone()
	}
	for _, entry := range output.Companions {
		if entry.Configuration != "" {
			if _, exists := output.Agents[entry.Configuration]; !exists {
				return Directory{}, ErrInvalid
			}
		}
	}
	return output, nil
}

func copyPointer[T any](input *T) *T {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func copyList[T any](input *[]T) *[]T {
	if input == nil {
		return nil
	}
	entries := slices.Clone(*input)
	if entries == nil {
		entries = []T{}
	}
	return &entries
}

func copyRetryPolicy(input *RetryConfig) *RetryConfig {
	if input == nil {
		return nil
	}
	output := *input
	output.Mode = copyPointer(input.Mode)
	output.MaxRetries = copyPointer(input.MaxRetries)
	output.RetryableCodes = copyList(input.RetryableCodes)
	if input.Backoff != nil {
		output.Backoff = &llm.BackoffConfig{
			InitialDelayMS: copyPointer(input.Backoff.InitialDelayMS),
			MaxDelayMS:     copyPointer(input.Backoff.MaxDelayMS),
			JitterRatio:    copyPointer(input.Backoff.JitterRatio),
		}
	}
	return &output
}

func valuesEqual(left Directory, right Directory) bool {
	return reflect.DeepEqual(left.Clone(), right.Clone())
}

// Groups returns the explicit identities in stable capability/name order.
func (input Directory) Groups() []Group {
	output := make([]Group, 0, len(input.Providers)+len(input.Prompts)+len(input.MCP)+len(input.Tools)+len(input.Companions)+len(input.Agents))
	for name := range input.Agents {
		output = append(output, Group{
			Section: AgentSection,
			Name:    name,
		})
	}
	for name := range input.Companions {
		output = append(output, Group{
			Section: CompanionSection,
			Name:    name,
		})
	}
	for name := range input.Providers {
		output = append(output, Group{
			Section: ProviderSection,
			Name:    name,
		})
	}
	for name := range input.Prompts {
		output = append(output, Group{
			Section: PromptSection,
			Name:    name,
		})
	}
	for name := range input.MCP {
		output = append(output, Group{
			Section: MCPSection,
			Name:    name,
		})
	}
	for name := range input.Tools {
		output = append(output, Group{
			Section: ToolSection,
			Name:    name,
		})
	}
	sort.Slice(output, func(left, right int) bool {
		if output[left].Section != output[right].Section {
			return output[left].Section < output[right].Section
		}
		return output[left].Name < output[right].Name
	})
	return output
}
