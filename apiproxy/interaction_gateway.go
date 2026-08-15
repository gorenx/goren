package apiproxy

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/connection"
	"github.com/gorenx/goren/plugin"
	"github.com/gorenx/goren/userquestions"
)

var errInteractionGatewayClosed = errors.New("apiproxy: interaction gateway closed")

// InteractionGatewayDependencies are the response table, mux frame broker,
// and human-question capability connected by the Host API adapter.
type InteractionGatewayDependencies struct {
	Methods       *Catalog
	Frames        InteractionFrameBroker
	UserQuestions userquestions.UserQuestions
}

// InteractionGatewayOptions supplies process-local identity and contained
// observer-failure handling.
type InteractionGatewayOptions struct {
	NewRPCID      func() (connection.RPCID, error)
	ObserverError func(error)
}

// InteractionGateway owns approval and question request correlation between
// the core capabilities and /api/respond. It does not own either capability's
// policy, validation prerequisites, or durable audit events.
type InteractionGateway struct {
	methods       *Catalog
	frames        InteractionFrameBroker
	newRPC        func() (connection.RPCID, error)
	reportFailure func(error)

	mu        sync.Mutex
	closed    bool
	approvals map[connection.RPCID]*pendingApproval
	questions map[connection.RPCID]*pendingQuestion
}

type interactionSettlement struct {
	once sync.Once
	done chan struct{}
}

func newInteractionSettlement() interactionSettlement {
	return interactionSettlement{done: make(chan struct{})}
}

func (settlement *interactionSettlement) complete(operation func()) {
	settlement.once.Do(func() {
		if operation != nil {
			operation()
		}
		close(settlement.done)
	})
}

// NewInteractionGateway installs the source-owned approval answerer and
// UserQuestions provider for the lifetime of ownerScope.
func NewInteractionGateway(
	requestContext context.Context,
	ownerScope *plugin.Scope,
	ports InteractionGatewayDependencies,
	settings InteractionGatewayOptions,
) (*InteractionGateway, error) {
	if requestContext == nil || ownerScope == nil {
		return nil, errors.New("apiproxy: Interaction Gateway Context and Scope are required")
	}
	if ports.Methods == nil || ports.Frames == nil || ports.UserQuestions == nil {
		return nil, errors.New("apiproxy: Interaction Gateway dependencies are incomplete")
	}
	newRPC := settings.NewRPCID
	if newRPC == nil {
		newRPC = mintFrameRPCID
	}
	reportFailure := settings.ObserverError
	if reportFailure == nil {
		reportFailure = func(error) {}
	}
	owner := &InteractionGateway{
		methods: ports.Methods, frames: ports.Frames, newRPC: newRPC, reportFailure: reportFailure,
		approvals: make(map[connection.RPCID]*pendingApproval),
		questions: make(map[connection.RPCID]*pendingQuestion),
	}

	releaseQuestions, err := ports.UserQuestions.RegisterProvider(requestContext, ownerScope, owner)
	if err != nil {
		return nil, err
	}
	releaseApprovals, err := approval.OnRequest(ownerScope, owner.answerApproval)
	if err != nil {
		return nil, errors.Join(err, releaseQuestions(context.Background()))
	}
	if _, err := plugin.Own(ownerScope, "apiProxy.interactionGateway()", owner.close); err != nil {
		return nil, errors.Join(
			err,
			releaseApprovals(context.Background()),
			releaseQuestions(context.Background()),
		)
	}
	if err := requestContext.Err(); err != nil {
		return nil, err
	}
	return owner, nil
}

func (owner *InteractionGateway) close(closeContext context.Context) error {
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil
	}
	owner.closed = true
	approvalEntries := make([]*pendingApproval, 0, len(owner.approvals))
	for _, entry := range owner.approvals {
		approvalEntries = append(approvalEntries, entry)
	}
	questionEntries := make([]*pendingQuestion, 0, len(owner.questions))
	for _, entry := range owner.questions {
		questionEntries = append(questionEntries, entry)
	}
	owner.mu.Unlock()

	for _, entry := range questionEntries {
		if entry.waiting.Withdraw(errInteractionGatewayClosed) {
			entry.finish(owner, QuestionCancelled)
		}
	}
	for _, entry := range approvalEntries {
		if entry.waiting.Withdraw(errInteractionGatewayClosed) {
			entry.finish(owner, approval.OutcomeCancelled)
		}
	}
	for _, entry := range questionEntries {
		select {
		case <-entry.settlement.done:
		case <-closeContext.Done():
			return closeContext.Err()
		}
	}
	for _, entry := range approvalEntries {
		select {
		case <-entry.settlement.done:
		case <-closeContext.Done():
			return closeContext.Err()
		}
	}
	return nil
}

func (owner *InteractionGateway) report(cause error) {
	if cause != nil {
		owner.reportFailure(fmt.Errorf("apiproxy: interaction frame delivery: %w", cause))
	}
}
