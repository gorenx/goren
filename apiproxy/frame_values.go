package apiproxy

func cloneStringPointer(source *string) *string {
	if source == nil {
		return nil
	}
	copyValue := *source
	return &copyValue
}
