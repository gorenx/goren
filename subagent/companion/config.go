// Package companion defines the configuration and operations of parent-owned Agents.
// Execution, messages and persistence remain with the ordinary Agent capabilities.
package companion

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/systemprompt"
	"github.com/gorenx/goren/tools"
)

// ToolInheritance is an explicit, complete parent-tool selection.
type ToolInheritance string

const (
	InheritNoTools  ToolInheritance = "none"
	InheritAllTools ToolInheritance = "all"
)

// Inheritance selects detached parent configuration; omitted selections inherit nothing.
type Inheritance struct {
	Model  bool            `json:"model,omitempty"`
	Prompt bool            `json:"prompt,omitempty"`
	Tools  ToolInheritance `json:"tools,omitempty"`
	MCP    bool            `json:"mcp,omitempty"`
}

// Config composes already-decoded capability configuration. Configuration
// loading and decoding remain with Settings and each capability owner.
type Config struct {
	Enabled bool                    `json:"enabled,omitempty"`
	Inherit Inheritance             `json:"inherit,omitempty"`
	Model   agent.Options           `json:"model,omitempty"`
	Prompt  systemprompt.Config     `json:"prompt,omitempty"`
	Tools   map[string]tools.Config `json:"tools,omitempty"`
	MCP     []mcpclient.Config      `json:"mcp,omitempty"`
}

// ErrInvalid rejects malformed configuration or incomplete creation input.
var ErrInvalid = errors.New("companion: invalid configuration or request")

// ValidateName checks a reusable configuration key without imposing Session identity rules.
func ValidateName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return ErrInvalid
	}
	return nil
}

// Validate checks values before acquiring any Agent or provider resources.
// Final tool membership is checked again after MCP discovery by Tools.
func (input Config) Validate() error {
	switch input.Inherit.Tools {
	case "", InheritNoTools, InheritAllTools:
	default:
		return fmt.Errorf("%w: tools inheritance must be none or all", ErrInvalid)
	}
	if err := input.Model.Validate(); err != nil {
		return err
	}
	if _, err := systemprompt.ValidateConfig(input.Prompt); err != nil {
		return err
	}
	for name := range input.Tools {
		if ValidateName(name) != nil || name == "report" {
			return fmt.Errorf("%w: invalid or duplicate dedicated tool %q", ErrInvalid, name)
		}
	}
	seen := make(map[string]bool)
	for _, connection := range input.MCP {
		if _, err := connection.Validate(); err != nil {
			return err
		}
		if seen[connection.ServerName] {
			return fmt.Errorf("%w: duplicate MCP name %q", ErrInvalid, connection.ServerName)
		}
		seen[connection.ServerName] = true
	}
	return nil
}

// Clone detaches all mutable configuration, preserving omission semantics.
func (input Config) Clone() Config {
	input.Model = input.Model.Clone()
	input.Prompt = input.Prompt.Clone()
	input.Tools = maps.Clone(input.Tools)
	for name, flags := range input.Tools {
		input.Tools[name] = flags.Clone()
	}
	input.MCP = slices.Clone(input.MCP)
	for index := range input.MCP {
		input.MCP[index].Args = slices.Clone(input.MCP[index].Args)
	}
	return input
}
