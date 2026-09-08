package commands

func cloneDefinition(original Definition) Definition {
	return Definition{
		Name:        original.Name,
		Description: original.Description,
		Input:       cloneInput(original.Input),
		RecordInput: cloneBool(original.RecordInput),
	}
}

func cloneDescriptor(original Descriptor) Descriptor {
	return Descriptor{
		Name:        original.Name,
		Description: original.Description,
		Input:       cloneInput(original.Input),
	}
}

func cloneInput(original *InputDescriptor) *InputDescriptor {
	if original == nil {
		return nil
	}
	detached := *original
	return &detached
}

func cloneResult(original Result) Result {
	return Result{
		Kind:           original.Kind,
		Text:           cloneString(original.Text),
		SourceEventSeq: cloneInt64(original.SourceEventSeq),
	}
}

func cloneString(original *string) *string {
	if original == nil {
		return nil
	}
	detached := *original
	return &detached
}

func cloneInt64(original *int64) *int64 {
	if original == nil {
		return nil
	}
	detached := *original
	return &detached
}

func cloneBool(original *bool) *bool {
	if original == nil {
		return nil
	}
	detached := *original
	return &detached
}

// Clone returns independent command metadata for publication across runtime boundaries.
func (specification Definition) Clone() Definition { return cloneDefinition(specification) }
