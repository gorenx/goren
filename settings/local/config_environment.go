package local

import "strconv"

func localEnvironment(lookup func(string) (string, bool)) (Config, error) {
	var decoded Config
	if lookup == nil {
		return decoded, nil
	}
	for _, entry := range []struct {
		name  string
		field **string
	}{
		{
			name:  "GOREN_LISTEN",
			field: &decoded.Server.Listen,
		},
		{
			name:  "GOREN_DATA_DIR",
			field: &decoded.Storage.DataDirectory,
		},
		{
			name:  "GOREN_SESSION_DB",
			field: &decoded.Storage.SessionDatabase,
		},
		{
			name:  "GOREN_WORKSPACE_DB",
			field: &decoded.Storage.WorkspaceDatabase,
		},
		{
			name:  "GOREN_QUERY_DB",
			field: &decoded.Storage.QueryDatabase,
		},
		{
			name:  "GOREN_CACHE_DB",
			field: &decoded.Storage.CacheDatabase,
		},
		{
			name:  "GOREN_CREDENTIALS_PATH",
			field: &decoded.Storage.CredentialsPath,
		},
		{
			name:  "GOREN_SETTINGS_DB",
			field: &decoded.Settings.Database,
		},
		{
			name:  "GOREN_LLM_PROVIDER",
			field: &decoded.LLM.Default.Provider,
		},
		{
			name:  "GOREN_LLM_MODEL",
			field: &decoded.LLM.Default.Model,
		},
		{
			name:  "DEEPSEEK_BASE_URL",
			field: &decoded.LLM.DeepSeek.BaseURL,
		},
	} {
		if value, exists := lookup(entry.name); exists {
			*entry.field = &value
		}
	}
	if raw, exists := lookup("GOREN_REQUEST_TIMEOUT_MS"); exists {
		for _, character := range raw {
			if character < '0' || character > '9' {
				return Config{}, ErrInvalid
			}
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Config{}, ErrInvalid
		}
		decoded.Server.RequestTimeoutMS = &value
	}
	if err := decoded.validate(); err != nil {
		return Config{}, err
	}
	return decoded, nil
}
