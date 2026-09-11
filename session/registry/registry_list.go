package registry

import (
	"sort"

	sessionactor "github.com/gorenx/goren/session/actor"
)

func (owner *RegistryGrain) listSessions() Sessions {
	result := Sessions{
		Items: make([]Session, 0),
	}
	for identifier, entry := range owner.sessions {
		grant, found := owner.directory.registrations[identifier]
		if !found || grant.retired || grant.creationID != entry.creationID || entry.header == nil {
			continue
		}
		result.Items = append(result.Items, Session{
			Header:     entry.header.Clone(),
			CreationID: entry.creationID,
		})
	}
	sort.Slice(result.Items, func(left, right int) bool {
		return result.Items[left].Header.ID < result.Items[right].Header.ID
	})
	return result
}

func (owner *RegistryGrain) resolveSession(input ResolveSession) Endpoint {
	entry := owner.sessions[input.SessionID]
	grant, found := owner.directory.registrations[input.SessionID]
	if !found ||
		grant.retired ||
		grant.creationID != input.CreationID ||
		entry == nil ||
		entry.creationID != input.CreationID ||
		entry.header == nil ||
		entry.endpoint == nil {
		return Endpoint{
			Err: sessionactor.ErrCreation,
		}
	}
	// GrainIdentity is immutable and contains sync.Once; share its address.
	return Endpoint{
		Target: entry.endpoint,
	}
}

func (owner *RegistryGrain) findSession(input FindSession) FoundSession {
	entry := owner.sessions[input.SessionID]
	grant, found := owner.directory.registrations[input.SessionID]
	if !found || grant.retired || entry == nil || grant.creationID != entry.creationID || entry.header == nil {
		return FoundSession{}
	}
	return FoundSession{
		Session: &Session{
			Header:     entry.header.Clone(),
			CreationID: entry.creationID,
		},
	}
}
