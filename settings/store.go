package settings

import "context"

// Store is the conditional persistence capability required by user settings.
// Commit must atomically compare the revision and replace the supplied snapshot.
// It assigns no business revisions and does not merge or validate owner values.
type Store interface {
	Load(context.Context) (State, error)
	Commit(context.Context, ChangeSet) error
}

// ChangeSet describes an atomic settings submission without database concepts.
type ChangeSet struct {
	ExpectedRevision int64
	Next             State
}
