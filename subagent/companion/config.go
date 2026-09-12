// Package companion defines the configuration and operations of parent-owned Agents.
// Execution, messages and persistence remain with the ordinary Agent capabilities.
package companion

import (
	"errors"
	"fmt"
	"strings"
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

// Config selects a named Settings configuration and explicit parent inheritance.
// Capability values belong to Settings; this value only describes creation policy.
type Config struct {
	Enabled       bool        `json:"enabled,omitempty"`
	Inherit       Inheritance `json:"inherit,omitempty"`
	Configuration string      `json:"configuration,omitempty"`
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
	if input.Configuration != "" && ValidateName(input.Configuration) != nil {
		return ErrInvalid
	}
	return nil
}

// Clone returns the creation policy, which contains no mutable capability values.
func (input Config) Clone() Config { return input }
