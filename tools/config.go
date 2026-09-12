package tools

// Config is a complete user-editable tool configuration. Omitted flags use
// capability defaults, not flags from another configuration group. Execution
// guards, middleware, schemas and Provider bindings are configured separately.
type Config struct {
	Enabled *bool `json:"enabled,omitempty"`
	Visible *bool `json:"visible,omitempty"`
}

// Resolve returns enablement and model visibility, both enabled by default.
func (input Config) Resolve() (bool, bool) {
	enabled := true
	visible := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.Visible != nil {
		visible = *input.Visible
	}
	return enabled, visible
}

// Clone detaches optional flags from a caller's mutable configuration.
func (input Config) Clone() Config {
	output := Config{}
	if input.Enabled != nil {
		flag := *input.Enabled
		output.Enabled = &flag
	}
	if input.Visible != nil {
		flag := *input.Visible
		output.Visible = &flag
	}
	return output
}
