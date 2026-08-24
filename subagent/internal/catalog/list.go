package catalog

import (
	"context"

	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

type sessionRecord struct {
	header  session.Header
	live    session.Context
	ordinal int
}

type listing struct {
	dependencies dependencies
	corpus       map[session.SessionID]sessionRecord
	parents      map[session.SessionID]struct{}
}

type positioned struct {
	session  sessionRecord
	parentID session.SessionID
	depth    int64
}

// ListChildren returns one parent's origin-classified direct children.
func (owner *Service) ListChildren(
	requestContext context.Context,
	parentID session.SessionID,
) ([]subagent.ListEntry, error) {
	prepared, err := owner.prepare(requestContext)
	if err != nil {
		return nil, err
	}
	candidates := make([]sessionRecord, 0)
	for _, candidate := range prepared.corpus {
		if candidate.header.ParentSession != nil &&
			*candidate.header.ParentSession == parentID &&
			candidate.header.Origin == session.OriginSubagent {
			candidates = append(candidates, candidate)
		}
	}
	newSiblingOrder().sort(candidates)
	return resolveRows(requestContext, candidates, prepared)
}

// ListDescendants returns every origin-classified descendant in stable
// pre-order while retaining ordinary Sessions as traversal nodes.
func (owner *Service) ListDescendants(
	requestContext context.Context,
	rootID session.SessionID,
) ([]subagent.DescendantListEntry, error) {
	prepared, err := owner.prepare(requestContext)
	if err != nil {
		return nil, err
	}
	positions := descendantRecords(prepared.corpus, rootID)
	candidates := make([]sessionRecord, len(positions))
	for index, position := range positions {
		candidates[index] = position.session
	}
	rows, err := resolveAligned(requestContext, candidates, prepared)
	if err != nil {
		return nil, err
	}
	entries := make([]subagent.DescendantListEntry, 0, len(rows))
	for index, row := range rows {
		if row == nil {
			continue
		}
		entries = append(
			entries,
			subagent.DescendantListEntry{
				Entry:    row,
				ParentID: positions[index].parentID,
				Depth:    positions[index].depth,
			},
		)
	}
	return entries, nil
}

func (owner *Service) prepare(requestContext context.Context) (listing, error) {
	dependencySet := owner.snapshot()
	if err := requireListing(requestContext, dependencySet); err != nil {
		return listing{}, err
	}
	persisted := []session.Header(nil)
	if dependencySet.persistence != nil {
		var err error
		persisted, err = dependencySet.persistence.List(requestContext)
		if requestErr := requestContext.Err(); requestErr != nil {
			return listing{}, cancelled(requestErr)
		}
		if err != nil {
			return listing{}, err
		}
	}
	corpus := make(map[session.SessionID]sessionRecord, len(persisted))
	nextOrdinal := 0
	for _, header := range persisted {
		ordinal := nextOrdinal
		if current, found := corpus[header.ID]; found {
			ordinal = current.ordinal
		} else {
			nextOrdinal++
		}
		corpus[header.ID] = sessionRecord{
			header:  header,
			ordinal: ordinal,
		}
	}
	for _, conversation := range dependencySet.sessions.List() {
		if conversation == nil {
			continue
		}
		header := conversation.Header()
		ordinal := nextOrdinal
		if current, found := corpus[header.ID]; found {
			ordinal = current.ordinal
		} else {
			nextOrdinal++
		}
		corpus[header.ID] = sessionRecord{
			header:  header,
			live:    conversation,
			ordinal: ordinal,
		}
	}
	parents := make(map[session.SessionID]struct{})
	for _, candidate := range corpus {
		if candidate.header.Origin == session.OriginSubagent &&
			candidate.header.ParentSession != nil {
			parents[*candidate.header.ParentSession] = struct{}{}
		}
	}
	return listing{
		dependencies: dependencySet,
		corpus:       corpus,
		parents:      parents,
	}, nil
}

func descendantRecords(
	corpus map[session.SessionID]sessionRecord,
	rootID session.SessionID,
) []positioned {
	children := make(map[session.SessionID][]sessionRecord)
	for _, candidate := range corpus {
		if candidate.header.ParentSession == nil {
			continue
		}
		parentID := *candidate.header.ParentSession
		children[parentID] = append(children[parentID], candidate)
	}
	ordering := newSiblingOrder()
	for parentID := range children {
		ordering.sort(children[parentID])
	}
	stack := make([]positioned, 0)
	direct := children[rootID]
	for index := len(direct) - 1; index >= 0; index-- {
		stack = append(stack, positioned{
			session:  direct[index],
			parentID: rootID,
			depth:    1,
		})
	}
	visited := map[session.SessionID]struct{}{
		rootID: {},
	}
	result := make([]positioned, 0)
	for len(stack) != 0 {
		last := len(stack) - 1
		position := stack[last]
		stack = stack[:last]
		identifier := position.session.header.ID
		if _, repeated := visited[identifier]; repeated {
			continue
		}
		visited[identifier] = struct{}{}
		if position.session.header.Origin == session.OriginSubagent {
			result = append(result, position)
		}
		descendants := children[identifier]
		for index := len(descendants) - 1; index >= 0; index-- {
			stack = append(stack, positioned{
				session:  descendants[index],
				parentID: identifier,
				depth:    position.depth + 1,
			})
		}
	}
	return result
}
