package bound

import (
	"errors"
)

// Creation is the complete command for creating one globally named
// Definition. The committed first revision is always 1.
type Creation struct {
	Definition Draft `json:"definition"`
}

func (target *Creation) UnmarshalJSON(rawValue []byte) error {
	var commandValue struct {
		Definition *Draft `json:"definition"`
	}
	if err := decodeDefinitionJSON(rawValue, &commandValue); err != nil {
		return err
	}
	if commandValue.Definition == nil {
		return errors.New(
			"subagent/bound: Creation definition is required",
		)
	}
	target.Definition = *commandValue.Definition
	return nil
}

// Replacement is the complete compare-and-swap command for one existing
// Definition identity.
type Replacement struct {
	ExpectedRevision int64 `json:"expectedRevision"`
	Definition       Draft `json:"definition"`
}

func (target *Replacement) UnmarshalJSON(rawValue []byte) error {
	var commandValue struct {
		ExpectedRevision jsonField[int64] `json:"expectedRevision"`
		Definition       *Draft           `json:"definition"`
	}
	if err := decodeDefinitionJSON(rawValue, &commandValue); err != nil {
		return err
	}
	if !commandValue.ExpectedRevision.present ||
		commandValue.ExpectedRevision.null ||
		commandValue.ExpectedRevision.value <= 0 ||
		commandValue.ExpectedRevision.value >= maximumSafeInteger {
		return errors.New(
			"subagent/bound: expected revision must permit one safe next revision",
		)
	}
	if commandValue.Definition == nil {
		return errors.New(
			"subagent/bound: Replacement definition is required",
		)
	}
	*target = Replacement{
		ExpectedRevision: commandValue.ExpectedRevision.value,
		Definition:       *commandValue.Definition,
	}
	return nil
}
