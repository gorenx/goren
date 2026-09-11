package llmactor

func queryFailure(command any, id RequestID, err error) any {
	switch command.(type) {
	case ListModels:
		return ModelsListed{
			RequestID: id,
			Err:       err,
		}
	case ResolveModel:
		return ModelResolved{
			RequestID: id,
			Err:       err,
		}
	case ResolveConfig:
		return ConfigResolved{
			RequestID: id,
			Err:       err,
		}
	default:
		return Completion{
			Err: err,
		}
	}
}
