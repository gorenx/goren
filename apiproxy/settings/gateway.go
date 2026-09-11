package settingsapi

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/gorenx/goren/apiproxy/rpc"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/settings"
)

const settingsAbsentMessage = "settings service is absent: this deployment does not mount a settings provider (e.g. @deepseek-ai/dsh-settings-file) in its composition"

// Gateway maps the Settings business capability to the Host wire contract.
// It owns neither user state nor Actor/SQLite lifetimes.
type Gateway struct {
	service settings.Settings
}

// NewGateway accepts a domain implementation, including settings/actor.Client.
// Nil preserves the existing absent-service failure.
func NewGateway(source settings.Settings) *Gateway {
	return &Gateway{
		service: source,
	}
}

// Describe projects local, user and target values without exposing credentials.
func (owner *Gateway) Describe(ctx context.Context, _ rpc.Request[DescribeRequest]) (rpc.Outcome[DescribeValue], error) {
	if owner.service == nil {
		return rpc.Fail[DescribeValue](rpc.NewRPCError(connection.ErrorInternal, settingsAbsentMessage, struct{}{})), nil
	}
	projection, err := owner.service.Describe(ctx)
	if err != nil {
		return rpc.Outcome[DescribeValue]{}, err
	}
	description, err := describeSnapshot(projection)
	if err != nil {
		return rpc.Outcome[DescribeValue]{}, err
	}
	return rpc.OK(description), nil
}

func describeSnapshot(projection settings.Snapshot) (DescribeValue, error) {
	known := make(map[settings.Group]struct{})
	for _, identifier := range projection.Base.Groups() {
		known[identifier] = struct{}{}
	}
	for _, identifier := range projection.Target.Groups() {
		known[identifier] = struct{}{}
	}
	for identifier := range projection.State.Versions {
		known[identifier] = struct{}{}
	}
	ordered := make([]settings.Group, 0, len(known))
	for identifier := range known {
		ordered = append(ordered, identifier)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].Section != ordered[right].Section {
			return ordered[left].Section < ordered[right].Section
		}
		return ordered[left].Name < ordered[right].Name
	})
	namespaces := make([]NamespaceView, 0, len(ordered))
	for _, identifier := range ordered {
		namespace, schema, mode := groupSchema(identifier.Section)
		base, err := json.Marshal(groupValue(projection.Base, identifier))
		if err != nil {
			return DescribeValue{}, settings.ErrInvalid
		}
		user, err := json.Marshal(groupValue(projection.State.User, identifier))
		if err != nil {
			return DescribeValue{}, settings.ErrInvalid
		}
		value, err := json.Marshal(groupValue(projection.Target, identifier))
		if err != nil {
			return DescribeValue{}, settings.ErrInvalid
		}
		baseJSON := json.RawMessage(base)
		userJSON := json.RawMessage(user)
		application := projection.Applications[identifier]
		failure := ""
		if application.Error != "" {
			failure = settings.ErrApplication.Error()
		}
		namespaces = append(namespaces, NamespaceView{
			NS:              namespace,
			Name:            identifier.Name,
			Schema:          json.RawMessage(schema),
			Value:           value,
			Base:            &baseJSON,
			User:            &userJSON,
			Applies:         mode,
			Secrets:         []SecretView{},
			Revision:        projection.State.Version(identifier),
			AppliedRevision: &application.AppliedRevision,
			PendingRestart:  application.PendingRestart,
			ApplyError:      failure,
		})
	}
	return DescribeValue{
		Writable:    true,
		HasDocument: false,
		Namespaces:  namespaces,
	}, nil
}

// groupValue erases the typed value only while mapping the outbound API view.
func groupValue(input settings.Values, identifier settings.Group) any {
	if !input.Has(identifier) {
		return nil
	}
	switch identifier.Section {
	case settings.CompanionSection:
		return input.Companions[identifier.Name]
	case settings.ToolSection:
		return input.Tools[identifier.Name]
	case settings.ProviderSection:
		return input.Providers[identifier.Name]
	case settings.PromptSection:
		return input.Prompts[identifier.Name]
	case settings.MCPSection:
		return input.MCP[identifier.Name]
	default:
		return nil
	}
}

func groupSchema(kind settings.Section) (string, string, ApplyMode) {
	switch kind {
	case settings.CompanionSection:
		return "companion", companionSchema, ApplyNewInstance
	case settings.ToolSection:
		return "tool", toolSchema, ApplyLive
	case settings.ProviderSection:
		return "llmprovider", providerSchema, ApplyRestart
	case settings.PromptSection:
		return "prompt", promptSchema, ApplyLive
	case settings.MCPSection:
		return "mcp", mcpSchema, ApplyLive
	default:
		return "", "{}", ApplyLive
	}
}

func groupView(projection settings.Snapshot, namespace string, name string) (NamespaceView, error) {
	description, err := describeSnapshot(projection)
	if err != nil {
		return NamespaceView{}, err
	}
	for _, entry := range description.Namespaces {
		if entry.NS == namespace && entry.Name == name {
			return entry, nil
		}
	}
	return NamespaceView{}, errors.New("settings: unknown group")
}

func businessSection(namespace string) settings.Section {
	switch namespace {
	case "companion":
		return settings.CompanionSection
	case "tool":
		return settings.ToolSection
	case "llmprovider":
		return settings.ProviderSection
	case "prompt":
		return settings.PromptSection
	case "mcp":
		return settings.MCPSection
	}
	return 0
}
