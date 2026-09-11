package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"

	"github.com/gorenx/goren/settings"
	"github.com/gorenx/goren/settings/internal/sqlite/internal/dbsql"
	"github.com/gorenx/goren/tools"
)

// Load reads revisions and all namespace rows from one transaction.
func (owner *Adapter) Load(ctx context.Context) (settings.State, error) {
	transaction, err := owner.database.BeginTx(ctx, &sql.TxOptions{
		ReadOnly: true,
	})
	if err != nil {
		return settings.State{}, settings.ErrStorage
	}
	defer transaction.Rollback()
	queries := owner.queries.WithTx(transaction)
	header, err := queries.ReadState(ctx)
	if err != nil || header.SchemaVersion != 1 {
		return settings.State{}, settings.ErrStorage
	}
	rows, err := queries.ReadSections(ctx)
	if err != nil {
		return settings.State{}, settings.ErrStorage
	}
	stored := settings.State{
		Revision: header.Revision,
	}
	for _, row := range rows {
		prefix, name, found := strings.Cut(row.Namespace, "/")
		if !found {
			return settings.State{}, settings.ErrStorage
		}
		identifier := settings.Group{
			Section: sectionKind(prefix),
			Name:    name,
		}
		if !identifier.Valid() {
			return settings.State{}, settings.ErrStorage
		}
		if stored.Versions == nil {
			stored.Versions = make(map[settings.Group]int64)
		}
		stored.Versions[identifier] = row.Revision
		if row.OverridesJson == "null" {
			continue
		}
		switch identifier.Section {
		case settings.ToolSection:
			if stored.User.Tools == nil {
				stored.User.Tools = make(map[string]tools.Config)
			}
			stored.User.Tools[name], err = settings.DecodeTool([]byte(row.OverridesJson))
		case settings.ProviderSection:
			if stored.User.Providers == nil {
				stored.User.Providers = make(map[string]settings.ProviderConfig)
			}
			stored.User.Providers[name], err = settings.DecodeProvider([]byte(row.OverridesJson))
		case settings.PromptSection:
			if stored.User.Prompts == nil {
				stored.User.Prompts = make(map[string]settings.PromptConfig)
			}
			stored.User.Prompts[name], err = settings.DecodePrompt([]byte(row.OverridesJson))
		case settings.MCPSection:
			if stored.User.MCP == nil {
				stored.User.MCP = make(map[string]settings.MCPConfig)
			}
			var decoded settings.MCPConfig
			decoded, err = settings.DecodeMCP([]byte(row.OverridesJson))
			if err == nil {
				err = decoded.Validate(name)
			}
			stored.User.MCP[name] = decoded
		}
		if err != nil {
			return settings.State{}, settings.ErrStorage
		}
	}
	if err := transaction.Commit(); err != nil {
		return settings.State{}, settings.ErrStorage
	}
	return stored, nil
}

// Commit executes the requested conditional write, without deriving revisions.
func (owner *Adapter) Commit(ctx context.Context, change settings.ChangeSet) error {
	transaction, err := owner.database.BeginTx(ctx, nil)
	if err != nil {
		return settings.ErrStorage
	}
	defer transaction.Rollback()
	queries := owner.queries.WithTx(transaction)
	changed, err := queries.CompareRevision(ctx, dbsql.CompareRevisionParams{
		NextRevision:     change.Next.Revision,
		ExpectedRevision: change.ExpectedRevision,
	})
	if err != nil {
		return settings.ErrStorage
	}
	if changed != 1 {
		return settings.ErrConflict
	}
	identifiers := make([]settings.Group, 0, len(change.Next.Versions))
	for identifier := range change.Next.Versions {
		identifiers = append(identifiers, identifier)
	}
	sort.Slice(identifiers, func(left, right int) bool {
		if identifiers[left].Section != identifiers[right].Section {
			return identifiers[left].Section < identifiers[right].Section
		}
		return identifiers[left].Name < identifiers[right].Name
	})
	for _, identifier := range identifiers {
		var value any
		if change.Next.User.Has(identifier) {
			switch identifier.Section {
			case settings.ToolSection:
				value = change.Next.User.Tools[identifier.Name]
			case settings.ProviderSection:
				value = change.Next.User.Providers[identifier.Name]
			case settings.PromptSection:
				value = change.Next.User.Prompts[identifier.Name]
			case settings.MCPSection:
				value = change.Next.User.MCP[identifier.Name]
			}
		}
		prefix := sectionName(identifier.Section)
		if prefix == "" {
			return settings.ErrStorage
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return settings.ErrStorage
		}
		if err := queries.WriteSection(ctx, dbsql.WriteSectionParams{
			Namespace:     prefix + "/" + identifier.Name,
			Revision:      change.Next.Version(identifier),
			OverridesJson: string(encoded),
		}); err != nil {
			return settings.ErrStorage
		}
	}
	if err := transaction.Commit(); err != nil {
		return settings.ErrUncertain
	}
	return nil
}

func sectionName(kind settings.Section) string {
	switch kind {
	case settings.ToolSection:
		return "tool"
	case settings.ProviderSection:
		return "llmprovider"
	case settings.PromptSection:
		return "prompt"
	case settings.MCPSection:
		return "mcp"
	default:
		return ""
	}
}

func sectionKind(name string) settings.Section {
	switch name {
	case "tool":
		return settings.ToolSection
	case "llmprovider":
		return settings.ProviderSection
	case "prompt":
		return settings.PromptSection
	case "mcp":
		return settings.MCPSection
	default:
		return 0
	}
}
