package local

import (
	"github.com/gorenx/goren/subagent/companion"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

func (input Config) validate() error {
	for name, entry := range input.Companions {
		if companion.ValidateName(name) != nil || entry.Validate() != nil {
			return ErrInvalid
		}
	}
	if input.SchemaVersion != nil && *input.SchemaVersion != 1 {
		return ErrInvalid
	}
	for _, field := range []*string{
		input.Storage.DataDirectory,
		input.Storage.SessionDatabase,
		input.Storage.WorkspaceDatabase,
		input.Storage.QueryDatabase,
		input.Storage.CacheDatabase,
		input.Storage.CredentialsPath,
		input.Settings.Database,
		input.LLM.Default.Provider,
		input.LLM.Default.Model,
	} {
		if field != nil {
			if strings.TrimSpace(*field) == "" || strings.ContainsRune(*field, 0) {
				return ErrInvalid
			}
		}
	}
	if input.Server.Listen != nil {
		_, port, err := net.SplitHostPort(*input.Server.Listen)
		if err != nil || strings.ContainsAny(*input.Server.Listen, " \t\r\n") {
			return ErrInvalid
		}
		for _, character := range port {
			if character < '0' || character > '9' {
				return ErrInvalid
			}
		}
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return ErrInvalid
		}
	}
	if input.Server.RequestTimeoutMS != nil {
		if *input.Server.RequestTimeoutMS <= 0 || *input.Server.RequestTimeoutMS > math.MaxInt64/int64(time.Millisecond) {
			return ErrInvalid
		}
	}
	if input.LLM.Default.MaxTokens != nil && *input.LLM.Default.MaxTokens <= 0 {
		return ErrInvalid
	}
	if input.LLM.Default.Temperature != nil {
		value := *input.LLM.Default.Temperature
		if math.IsNaN(value) ||
			math.IsInf(value, 0) ||
			value < 0 ||
			value > 2 {
			return ErrInvalid
		}
	}
	if err := input.LLM.DeepSeek.Validate(); err != nil {
		return err
	}
	if err := input.Prompt.Validate(); err != nil {
		return err
	}
	if input.MCP.Servers != nil {
		names := make(map[string]bool)
		for _, entry := range *input.MCP.Servers {
			if _, err := entry.Validate(); err != nil || names[entry.ServerName] {
				return ErrInvalid
			}
			names[entry.ServerName] = true
		}
	}
	if input.MCP.Files != nil {
		for _, path := range *input.MCP.Files {
			if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
				return ErrInvalid
			}
		}
	}
	return nil
}
