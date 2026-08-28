package connection

// Key is a Host method restricted to trusted loopback requests. The empty
// value records set membership only.
var privilegedMethods = map[string]struct{}{
	"agentPreset.read":         {},
	"agentPreset.copy":         {},
	"agentPreset.openDocument": {},
	"agentPreset.remove":       {},
	"host.pickDirectory":       {},
	"host.openPath":            {},
	"settings.describe":        {},
	"settings.openDocument":    {},
	"settings.update":          {},
	"settings.replace":         {},
	"settings.mutate":          {},
	"credentials.describe":     {},
	"credentials.set":          {},
	"credentials.unset":        {},
	"llm.discoverModels":       {},
	"bound.list":               {},
	"bound.create":             {},
	"bound.replace":            {},
}

func isPrivilegedMethod(method string) bool {
	_, exists := privilegedMethods[method]
	return exists
}
