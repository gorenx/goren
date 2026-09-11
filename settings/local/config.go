package local

import (
	"errors"
	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/mcpclient"
	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/tools"
)

// Config is the deployment file contract, before user overrides. It holds
// named values only; composition maps them to each runtime capability's config.
// Pointer fields distinguish omission from explicit empty, zero and false.
type Config struct {
	Tools         map[string]tools.Config `json:"tools,omitempty"`
	SchemaVersion *int                    `json:"schemaVersion,omitempty"`
	Server        ServerConfig            `json:"server,omitempty"`
	Storage       StorageConfig           `json:"storage,omitempty"`
	Settings      DatabaseConfig          `json:"settings,omitempty"`
	LLM           LLMConfig               `json:"llm,omitempty"`
	Prompt        settings.PromptConfig   `json:"prompt,omitempty"`
	MCP           MCPFiles                `json:"mcp,omitempty"`
}

// ServerConfig describes process inputs, not a Session's workspace directory.
type ServerConfig struct {
	Listen           *string `json:"listen,omitempty"`
	RequestTimeoutMS *int64  `json:"requestTimeoutMs,omitempty"`
}

// StorageConfig locates independently owned stores. Missing database paths are
// derived only after all local sources have been merged.
type StorageConfig struct {
	DataDirectory     *string `json:"dataDirectory,omitempty"`
	SessionDatabase   *string `json:"sessionDatabase,omitempty"`
	WorkspaceDatabase *string `json:"workspaceDatabase,omitempty"`
	QueryDatabase     *string `json:"queryDatabase,omitempty"`
	CacheDatabase     *string `json:"cacheDatabase,omitempty"`
	CredentialsPath   *string `json:"credentialsPath,omitempty"`
}

// DatabaseConfig locates user settings; the user layer cannot change this input.
type DatabaseConfig struct {
	Database *string `json:"database,omitempty"`
}

// LLMConfig separates the new-Session template from shared connection settings.
type LLMConfig struct {
	Default  ModelConfig             `json:"default,omitempty"`
	DeepSeek settings.ProviderConfig `json:"deepseek,omitempty"`
}

// ModelConfig is a sparse local default template. Existing Sessions retain their
// own recorded model selections; fields map to llm.CallConfig at composition.
type ModelConfig struct {
	Provider        *string                `json:"provider,omitempty"`
	Model           *string                `json:"model,omitempty"`
	ReasoningEffort *llm.ReasoningEffortID `json:"reasoningEffort,omitempty"`
	Temperature     *float64               `json:"temperature,omitempty"`
	MaxTokens       *int                   `json:"maxTokens,omitempty"`
	Stop            *[]string              `json:"stop,omitempty"`
}

// MCPFiles contains inline definitions and file references. Stdio is allowed in
// trusted local files; user-editable MCPConfig remains HTTP-only.
type MCPFiles struct {
	Servers *[]mcpclient.Config `json:"servers,omitempty"`
	Files   *[]string           `json:"files,omitempty"`
}

// ErrInvalid identifies invalid deployment input without exposing its values.
var ErrInvalid = errors.New("settings/local: invalid configuration")
