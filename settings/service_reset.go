package settings

import "context"

// ResetProvider clears a complete override and restores the installed local group.
func (owner *Service) ResetProvider(ctx context.Context, name string, expected int64) (Snapshot, error) {
	next := owner.state.User.Clone()
	delete(next.Providers, name)
	return owner.reset(ctx, Group{
		Section: ProviderSection,
		Name:    name,
	}, expected, next)
}

// ResetPrompt clears a complete override and restores the installed local group.
func (owner *Service) ResetPrompt(ctx context.Context, name string, expected int64) (Snapshot, error) {
	next := owner.state.User.Clone()
	delete(next.Prompts, name)
	return owner.reset(ctx, Group{
		Section: PromptSection,
		Name:    name,
	}, expected, next)
}

// ResetMCP clears a complete override, retaining its last submission version.
func (owner *Service) ResetMCP(ctx context.Context, name string, expected int64) (Snapshot, error) {
	next := owner.state.User.Clone()
	delete(next.MCP, name)
	return owner.reset(ctx, Group{
		Section: MCPSection,
		Name:    name,
	}, expected, next)
}

// ResetTool clears the complete override to restore its local tool configuration.
func (owner *Service) ResetTool(ctx context.Context, name string, expected int64) (Snapshot, error) {
	next := owner.state.User.Clone()
	delete(next.Tools, name)
	return owner.reset(ctx, Group{
		Section: ToolSection,
		Name:    name,
	}, expected, next)
}

func (owner *Service) reset(ctx context.Context, identifier Group, expected int64, overrides Directory) (Snapshot, error) {
	if !owner.target.Has(identifier) && owner.state.Version(identifier) == 0 {
		return owner.Snapshot(), ErrInvalid
	}
	return owner.update(ctx, identifier, expected, overrides)
}
