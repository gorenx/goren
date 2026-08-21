//go:build contract

package contract_test

import (
	"context"
	"testing"

	"github.com/gorenx/goren/plugin"
)

type contractEventFailureReporter struct {
	testingContext testing.TB
}

func (reporter contractEventFailureReporter) ReportEventFailure(
	_ context.Context,
	failure plugin.EventFailure,
) {
	reporter.testingContext.Helper()
	reporter.testingContext.Errorf(
		"best-effort Event %q delivery failed: %v",
		failure.EventName,
		failure.Error,
	)
}

func newContractRuntime(testingContext testing.TB) *plugin.Runtime {
	testingContext.Helper()
	return plugin.NewRuntime(plugin.RuntimeSettings{
		EventFailures: contractEventFailureReporter{
			testingContext: testingContext,
		},
	})
}
