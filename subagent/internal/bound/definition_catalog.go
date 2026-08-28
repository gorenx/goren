package bound

import (
	"context"
	"errors"
	"fmt"

	boundcontract "github.com/gorenx/goren/subagent/bound"
)

var (
	// ErrDefinitionExists is returned by DefinitionStore when Create loses the
	// stable-name uniqueness race.
	ErrDefinitionExists = errors.New("subagent: Bound Definition already exists")
	// ErrDefinitionNotFound is returned by DefinitionStore when Replace targets
	// a missing stable name.
	ErrDefinitionNotFound = errors.New("subagent: Bound Definition not found")
	// ErrDefinitionRevisionConflict is returned by DefinitionStore when a
	// compare-and-swap replacement observes another revision.
	ErrDefinitionRevisionConflict = errors.New(
		"subagent: Bound Definition revision conflict",
	)
)

// DefinitionStore is the persistence transaction intent consumed by the
// Definition catalog. Concrete rows and database errors remain adapter-owned.
type DefinitionStore interface {
	Load(context.Context) ([]boundcontract.Definition, error)
	Create(context.Context, boundcontract.Definition) error
	Replace(
		context.Context,
		int64,
		boundcontract.Definition,
	) error
	Close(context.Context) error
}

// DefinitionReconciler receives a level-triggered notification after the
// durable Definition index changes. It must retain pending work itself.
type DefinitionReconciler interface {
	DefinitionsChanged()
}

// definitionCatalog owns the committed in-process Definition index and the
// write ordering from Store CAS to index publication to live reconciliation.
type definitionCatalog struct {
	store      DefinitionStore
	index      *definitionIndex
	reconciler DefinitionReconciler
}

func newDefinitionCatalog(
	requestContext context.Context,
	persistence DefinitionStore,
	reconciler DefinitionReconciler,
) (*definitionCatalog, error) {
	if requestContext == nil {
		return nil, errors.New(
			"subagent: Bound Definition catalog Context is nil",
		)
	}
	if persistence == nil {
		return nil, errors.New(
			"subagent: Bound Definition catalog requires a Store",
		)
	}
	definitions, err := persistence.Load(requestContext)
	if err != nil {
		return nil, fmt.Errorf(
			"subagent: load Bound Definitions: %w",
			err,
		)
	}
	index, err := newDefinitionIndex(definitions)
	if err != nil {
		return nil, err
	}
	return &definitionCatalog{
		store:      persistence,
		index:      index,
		reconciler: reconciler,
	}, nil
}

func (owner *definitionCatalog) List(
	requestContext context.Context,
) ([]boundcontract.Definition, error) {
	if err := checkContext(requestContext, "Bound Definition List"); err != nil {
		return nil, err
	}
	return owner.index.all(), nil
}

func (owner *definitionCatalog) Create(
	requestContext context.Context,
	creation boundcontract.Creation,
) (boundcontract.Definition, error) {
	if err := checkContext(requestContext, "Bound Definition Create"); err != nil {
		return boundcontract.Definition{}, err
	}
	definitionValue, err := boundcontract.NewDefinition(creation.Definition, 1)
	if err != nil {
		return boundcontract.Definition{}, err
	}
	if err = owner.store.Create(requestContext, definitionValue); err != nil {
		return boundcontract.Definition{}, definitionStoreError(
			definitionValue.Name,
			1,
			err,
		)
	}
	owner.index.publish(definitionValue)
	owner.notifyReconciler()
	return boundcontract.SnapshotDefinition(definitionValue)
}

func (owner *definitionCatalog) Replace(
	requestContext context.Context,
	replacement boundcontract.Replacement,
) (boundcontract.Definition, error) {
	if err := checkContext(requestContext, "Bound Definition Replace"); err != nil {
		return boundcontract.Definition{}, err
	}
	const maximumRevision = int64(1<<53 - 1)
	if replacement.ExpectedRevision <= 0 ||
		replacement.ExpectedRevision >= maximumRevision {
		return boundcontract.Definition{}, errors.New(
			"subagent: Bound Definition expected revision must permit one safe next revision",
		)
	}
	definitionValue, err := boundcontract.NewDefinition(
		replacement.Definition,
		replacement.ExpectedRevision+1,
	)
	if err != nil {
		return boundcontract.Definition{}, err
	}
	if err = owner.store.Replace(
		requestContext,
		replacement.ExpectedRevision,
		definitionValue,
	); err != nil {
		return boundcontract.Definition{}, definitionStoreError(
			definitionValue.Name,
			replacement.ExpectedRevision,
			err,
		)
	}
	owner.index.publish(definitionValue)
	owner.notifyReconciler()
	return boundcontract.SnapshotDefinition(definitionValue)
}

func (owner *definitionCatalog) enabled() []boundcontract.Definition {
	return owner.index.enabled()
}

func (owner *definitionCatalog) find(
	definitionName string,
) (boundcontract.Definition, bool) {
	return owner.index.find(definitionName)
}

func (owner *definitionCatalog) Close(closeContext context.Context) error {
	return owner.store.Close(closeContext)
}

func (owner *definitionCatalog) notifyReconciler() {
	if owner.reconciler != nil {
		owner.reconciler.DefinitionsChanged()
	}
}

func definitionStoreError(
	definitionName string,
	expectedRevision int64,
	storeErr error,
) error {
	switch {
	case errors.Is(storeErr, ErrDefinitionExists):
		return &boundcontract.Error{
			Code: boundcontract.ErrorDefinitionExists,
			Message: fmt.Sprintf(
				"Bound Definition %q already exists",
				definitionName,
			),
			Cause: storeErr,
		}
	case errors.Is(storeErr, ErrDefinitionNotFound):
		return &boundcontract.Error{
			Code: boundcontract.ErrorDefinitionNotFound,
			Message: fmt.Sprintf(
				"Bound Definition %q does not exist",
				definitionName,
			),
			Cause: storeErr,
		}
	case errors.Is(storeErr, ErrDefinitionRevisionConflict):
		return &boundcontract.Error{
			Code: boundcontract.ErrorDefinitionConflict,
			Message: fmt.Sprintf(
				"Bound Definition %q revision does not match expected %d",
				definitionName,
				expectedRevision,
			),
			Cause: storeErr,
		}
	default:
		return storeErr
	}
}

var _ boundcontract.Definitions = (*definitionCatalog)(nil)
