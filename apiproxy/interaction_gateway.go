package apiproxy

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/gorenx/goren/approval"
	"github.com/gorenx/goren/connection"
)

var errInteractionGatewayClosed = errors.New("apiproxy: interaction gateway closed")

// InteractionGatewayDependencies are the response table, mux frame broker,
// and human-question capability connected by the Host API adapter.
type InteractionGatewayDependencies struct {
	Methods *Catalog
	Frames  InteractionFrameBroker
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

// NewInteractionGateway constructs approval and question correlation state.
// The owning API Proxy Plugin declares the approval Waterfall, registers this
// object as the User Questions Provider, and owns Close.
func NewInteractionGateway(
	ports InteractionGatewayDependencies,
	settings InteractionGatewayOptions,
) (*InteractionGateway, error) {
	if ports.Methods == nil || ports.Frames == nil {
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
		methods:       ports.Methods,
		frames:        ports.Frames,
		newRPC:        newRPC,
		reportFailure: reportFailure,
		approvals:     make(map[connection.RPCID]*pendingApproval),
		questions:     make(map[connection.RPCID]*pendingQuestion),
	}
	return owner, nil
}

// Close cancels pending client interactions and waits for their settlement.
func (owner *InteractionGateway) Close(closeContext context.Context) error {
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
		entry.waiting.Withdraw(errInteractionGatewayClosed)
		entry.finish(owner, QuestionCancelled)
	}
	for _, entry := range approvalEntries {
		entry.waiting.Withdraw(errInteractionGatewayClosed)
		entry.finish(owner, approval.OutcomeCancelled)
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
