// Package credentialactor adapts Credentials business operations to GoAkt scheduling.
package credentialactor

import (
	"context"

	"github.com/gorenx/goren/credentials"
)

// Initialize installs the business owner from the composition root.
type Initialize struct {
	Manager *credentials.Manager
}

// Resolve requests a secret for one provider operation.
type Resolve struct {
	Context context.Context
	Ref     string
}

// Describe requests metadata without exposing a secret.
type Describe struct {
	Context context.Context
	Ref     string
}

// Set requests a managed credential write.
type Set struct {
	Context context.Context
	Ref     string
	Value   string
}

// Unset requests removal of a managed credential.
type Unset struct {
	Context context.Context
	Ref     string
}

// Resolution carries a resolved credential or its original business error.
type Resolution struct {
	Resolved credentials.Resolved
	Err      error
}

// Description contains only safe credential metadata and a business error.
type Description struct {
	Info credentials.Info
	Err  error
}

// Completion reports a completed mutation or lifecycle operation.
type Completion struct {
	Err error
}
