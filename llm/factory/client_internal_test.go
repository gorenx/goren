package factory

import (
	"slices"
	"testing"

	"github.com/gorenx/goren/llm"
)

func TestRegisterBuiltInAdaptersRegistersEveryImplementedAPI(t *testing.T) {
	adapterRegistry := llm.NewRegistry()
	if err := registerBuiltInAdapters(adapterRegistry, clientConfig{}); err != nil {
		t.Fatalf("register built-in adapters: %v", err)
	}
	registeredAPIs := adapterRegistry.APIs()
	wantedAPIs := []llm.API{llm.APIOpenAICompletions, llm.APIOpenAIResponses}
	if !slices.Equal(registeredAPIs, wantedAPIs) {
		t.Fatalf("got registered APIs %v, want %v", registeredAPIs, wantedAPIs)
	}
}
