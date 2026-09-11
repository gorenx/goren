package connection

// Key is a Host method restricted to trusted loopback requests. The empty
// value records set membership only.
var privilegedMethods = map[string]struct{}{
	"agentPreset.read":          {},
	"agentPreset.copy":          {},
	"agentPreset.openDocument":  {},
	"agentPreset.remove":        {},
	"host.pickDirectory":        {},
	"directoryPicker.pick":      {},
	"host.openPath":             {},
	"settings.describe":         {},
	"settings.openDocument":     {},
	"settings.update":           {},
	"settings.replace":          {},
	"settings.mutate":           {},
	"settings.updateProvider":   {},
	"settings.updatePrompt":     {},
	"settings.updateTool":       {},
	"settings.resetTool":        {},
	"settings.updateCompanion":  {},
	"settings.updateAgent":      {},
	"settings.resetCompanion":   {},
	"settings.updateMCP":        {},
	"settings.retryApplication": {},
	"settings.resetProvider":    {},
	"settings.resetPrompt":      {},
	"settings.resetMCP":         {},
	"credentials.describe":      {},
	"credentials.set":           {},
	"credentials.unset":         {},
	"llm.discoverModels":        {},
	"bound.list":                {},
	"bound.tools":               {},
	"bound.extensions":          {},
	"bound.create":              {},
	"bound.replace":             {},
}

func isPrivilegedMethod(method string) bool {
	_, exists := privilegedMethods[method]
	return exists
}
