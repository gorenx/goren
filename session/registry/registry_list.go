package registry

import "sort"

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
