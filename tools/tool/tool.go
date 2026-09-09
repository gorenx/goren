// Package tool owns one tool's implementation replacement and resource lifetime.
// Callers serialize access and drain executions before updating or closing it.
package tool

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"strings"

	"github.com/gorenx/goren/llm"
	"github.com/gorenx/goren/tools"
)

// Resource releases only resources exclusively owned by one implementation.
// After an error, Close must support retry; borrowed capabilities are not closed.
type Resource interface {
	Close(context.Context) error
}

// Provider constructs an implementation from bound configuration and borrowed
// capabilities. Each Open returns independent owned resources, including any
// partially constructed resources that still require cleanup when Open fails.
type Provider interface {
	Open(context.Context) (Implementation, error)
}

// Implementation combines tool behavior with optional owned resources.
// Each prepared implementation must own a distinct resource lifetime.
type Implementation struct {
	Definition tools.ToolDefinition
	Resource   Resource
}

// Revision identifies the unique update that installed an implementation.
// It is opaque, never ordered, and independent of the model-facing schema version.
type Revision string

// Identity identifies one committed implementation independently of its update request.
type Identity string

// Tool owns current, candidate and retiring implementations of one logical tool.
// It has no scheduler or framework references and must not be used concurrently.
type Tool struct {
	name     string
	revision Revision
	identity Identity
	current  *Implementation
	// candidate holds the validated replacement until the catalog update is published.
	// Provision creates it without replacing current or releasing current's resources.
	// After publication, Commit moves its body to current and moves the previous
	// current to retiring; Retire then releases the old resources.
	// Before publication, Abort releases only candidate and preserves current.
	// Failed cleanup retains candidate for retry, but never permits activation.
	candidate *candidate
	retiring  *Implementation
	closed    bool
}

type candidate struct {
	revision Revision
	body     Implementation
	// discardRequested prevents activation after cleanup may have partially succeeded.
	discardRequested bool
}

// New reserves a logical tool name without constructing implementation resources.
func New(name string) (*Tool, error) {
	if name == "" ||
		strings.TrimSpace(name) != name ||
		name == tools.RunCodeName {
		return nil, errors.New("tools: invalid tool name")
	}
	return &Tool{
		name: name,
	}, nil
}

// Name returns the immutable logical tool name, independent of implementation state.
func (owner *Tool) Name() string { return owner.name }

// acceptImplementation validates and retains a candidate after admission.
// Its caller keeps the Tool exclusively owned from admission through this call.
func (owner *Tool) acceptImplementation(target Revision, input Implementation) error {
	if input.Definition.Name != owner.name {
		return errors.New("tools: implementation has a different tool name")
	}
	detached, err := tools.SnapshotDefinition(input.Definition)
	if err != nil {
		return err
	}
	input.Definition = detached
	owner.candidate = &candidate{
		revision: target,
		body:     input,
	}
	return nil
}

func (owner *Tool) checkPreparation(target Revision) error {
	if owner.closed {
		return errors.New("tools: tool is closed")
	}
	if owner.candidate != nil || owner.retiring != nil {
		return errors.New("tools: previous implementation update is unsettled")
	}
	if target == "" || target == owner.revision {
		return errors.New("tools: stale or invalid implementation revision")
	}
	return nil
}

// Schema returns only the prepared candidate's detached model-facing definition.
// A candidate whose cleanup has started can no longer be registered for publication.
func (owner *Tool) Schema(target Revision) (llm.ToolSchema, error) {
	if owner.closed ||
		owner.candidate == nil ||
		owner.candidate.revision != target ||
		owner.candidate.discardRequested {
		return llm.ToolSchema{}, errors.New("tools: candidate definition is unavailable for this revision")
	}
	definition := owner.candidate.body.Definition
	return llm.ToolSchema{
		Name:        definition.Name,
		Description: definition.Description,
		Parameters:  append([]byte(nil), definition.Parameters...),
	}, nil
}

// Commit switches implementations after the caller confirms catalog publication.
// Retrying the committed revision does not switch or release resources twice.
func (owner *Tool) Commit(target Revision) error {
	if owner.closed {
		return errors.New("tools: tool is closed")
	}
	if owner.current != nil && owner.revision == target {
		return nil
	}
	if owner.candidate == nil ||
		owner.candidate.revision != target ||
		owner.candidate.discardRequested {
		return errors.New("tools: implementation revision is not prepared")
	}
	owner.retiring = owner.current
	owner.current = &owner.candidate.body
	owner.revision = target
	owner.identity = Identity(uuid.NewString())
	owner.candidate = nil
	return nil
}

// Retire releases the previous implementation and retains it on release failure.
func (owner *Tool) Retire(ctx context.Context, target Revision) error {
	if owner.revision != target || owner.current == nil {
		return errors.New("tools: implementation revision is not committed")
	}
	return release(ctx, &owner.retiring)
}

// Abort releases an unpublished candidate. Publication must be ruled out by the
// caller; this object cannot inspect the shared catalog's commit point.
func (owner *Tool) Abort(ctx context.Context, target Revision) error {
	if owner.candidate == nil {
		if owner.revision == target {
			return errors.New("tools: cannot abort a committed implementation")
		}
		return nil
	}
	if owner.candidate.revision != target {
		return errors.New("tools: candidate belongs to a different update")
	}
	owner.candidate.discardRequested = true
	err := closeResource(ctx, owner.candidate.body.Resource)
	if err != nil {
		return err
	}
	owner.candidate = nil
	return nil
}

// Definition returns a detached executable definition only for a settled implementation.
// The caller must finish every use before preparing an update or closing the Tool.
func (owner *Tool) Definition(target Revision) (tools.ToolDefinition, error) {
	if owner.closed ||
		owner.current == nil ||
		owner.candidate != nil ||
		owner.retiring != nil ||
		owner.revision != target {
		return tools.ToolDefinition{}, errors.New("tools: requested implementation is unavailable")
	}
	detached := owner.current.Definition
	detached.Parameters = append([]byte(nil), detached.Parameters...)
	detached.Output.Schema = append([]byte(nil), detached.Output.Schema...)
	return detached, nil
}

// Close permanently stops admission and attempts every owned release. Failures
// retain only the failed resources so another Close can finish the same cleanup.
func (owner *Tool) Close(ctx context.Context) error {
	owner.closed = true
	var failures []error
	if owner.candidate != nil {
		err := closeResource(ctx, owner.candidate.body.Resource)
		if err != nil {
			failures = append(failures, err)
		} else {
			owner.candidate = nil
		}
	}
	failures = append(failures, release(ctx, &owner.retiring))
	failures = append(failures, release(ctx, &owner.current))
	return errors.Join(failures...)
}

func release(ctx context.Context, retained **Implementation) error {
	if *retained == nil {
		return nil
	}
	err := closeResource(ctx, (*retained).Resource)
	if err != nil {
		return err
	}
	*retained = nil
	return nil
}

func closeResource(ctx context.Context, owned Resource) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if owned == nil {
		return nil
	}
	return owned.Close(ctx)
}

// Binding returns the current implementation identity and borrowed execution contract.
// The caller must drain executions before replacing or closing the implementation.
func (owner *Tool) Binding() (Identity, tools.Descriptor, error) {
	definition, err := owner.Definition(owner.revision)
	if err != nil {
		return "", tools.Descriptor{}, err
	}
	return owner.identity, definition.Describe(), nil
}
