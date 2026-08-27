package bound

import "github.com/gorenx/goren/session"

func (owner *Service) childOperation(
	parentID session.SessionID,
	childID session.SessionID,
) *operation {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	key := operationKey{
		parentID: parentID,
		childID:  childID,
	}
	current := owner.operations[key]
	if current == nil {
		current = &operation{}
		owner.operations[key] = current
	}
	return current
}

func (owner *Service) parentOperation(
	parentID session.SessionID,
) *operation {
	owner.mutex.Lock()
	defer owner.mutex.Unlock()
	current := owner.parents[parentID]
	if current == nil {
		current = &operation{}
		owner.parents[parentID] = current
	}
	return current
}
