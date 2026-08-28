package bound

import (
	"fmt"
	"reflect"
	"sort"
	"sync"

	boundcontract "github.com/gorenx/goren/subagent/bound"
)

// definitionIndex is a detached latest-revision read model. Store commit is
// the only authority that may publish into it.
type definitionIndex struct {
	mutex sync.RWMutex
	// Key is the stable Definition name. Value is the detached latest
	// Definition revision committed by the Store.
	values map[string]boundcontract.Definition
}

func newDefinitionIndex(
	definitions []boundcontract.Definition,
) (*definitionIndex, error) {
	index := &definitionIndex{
		values: make(map[string]boundcontract.Definition, len(definitions)),
	}
	for _, definitionValue := range definitions {
		validated, err := boundcontract.SnapshotDefinition(definitionValue)
		if err != nil {
			return nil, fmt.Errorf(
				"subagent: invalid stored Bound Definition: %w",
				err,
			)
		}
		if _, duplicate := index.values[validated.Name]; duplicate {
			return nil, fmt.Errorf(
				"subagent: duplicate stored Bound Definition %q",
				validated.Name,
			)
		}
		index.values[validated.Name] = validated
	}
	return index, nil
}

func (owner *definitionIndex) publish(
	definitionValue boundcontract.Definition,
) {
	detached, err := boundcontract.SnapshotDefinition(definitionValue)
	if err != nil {
		panic(err)
	}
	owner.mutex.Lock()
	current, found := owner.values[detached.Name]
	if found && current.Revision > detached.Revision {
		owner.mutex.Unlock()
		return
	}
	if found && current.Revision == detached.Revision &&
		!reflect.DeepEqual(current, detached) {
		owner.mutex.Unlock()
		panic("subagent: conflicting Bound Definition revision publication")
	}
	owner.values[detached.Name] = detached
	owner.mutex.Unlock()
}

func (owner *definitionIndex) all() []boundcontract.Definition {
	owner.mutex.RLock()
	result := make([]boundcontract.Definition, 0, len(owner.values))
	for _, definitionValue := range owner.values {
		detached, err := boundcontract.SnapshotDefinition(definitionValue)
		if err != nil {
			owner.mutex.RUnlock()
			panic(err)
		}
		result = append(result, detached)
	}
	owner.mutex.RUnlock()
	sort.Slice(result, func(leftIndex int, rightIndex int) bool {
		return result[leftIndex].Name < result[rightIndex].Name
	})
	return result
}

func (owner *definitionIndex) enabled() []boundcontract.Definition {
	allDefinitions := owner.all()
	result := make([]boundcontract.Definition, 0, len(allDefinitions))
	for _, definitionValue := range allDefinitions {
		if definitionValue.Enabled {
			result = append(result, definitionValue)
		}
	}
	return result
}

func (owner *definitionIndex) find(
	definitionName string,
) (boundcontract.Definition, bool) {
	owner.mutex.RLock()
	definitionValue, found := owner.values[definitionName]
	owner.mutex.RUnlock()
	if !found {
		return boundcontract.Definition{}, false
	}
	detached, err := boundcontract.SnapshotDefinition(definitionValue)
	if err != nil {
		panic(err)
	}
	return detached, true
}
