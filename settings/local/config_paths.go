package local

import (
	"errors"
	"os"
	"path/filepath"
)

func (input *Config) resolvePaths(directory string) {
	for _, field := range []**string{
		&input.Storage.DataDirectory,
		&input.Storage.SessionDatabase,
		&input.Storage.WorkspaceDatabase,
		&input.Storage.QueryDatabase,
		&input.Storage.CacheDatabase,
		&input.Storage.CredentialsPath,
		&input.Settings.Database,
	} {
		if *field != nil {
			resolved := localPath(directory, **field)
			*field = &resolved
		}
	}
	if input.MCP.Files != nil {
		resolved := make([]string, len(*input.MCP.Files))
		for index, path := range *input.MCP.Files {
			resolved[index] = localPath(directory, path)
		}
		input.MCP.Files = &resolved
	}
}

func localPath(directory string, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(directory, path)
}

func (input *Config) storageDefaults() error {
	if input.Storage.DataDirectory == nil {
		directory, err := os.UserConfigDir()
		if err != nil {
			return errors.New("settings: dataDirectory is required when the user config directory is unavailable")
		}
		path, err := filepath.Abs(filepath.Join(directory, "goren"))
		if err != nil {
			return ErrInvalid
		}
		input.Storage.DataDirectory = &path
	}
	fillPath(&input.Storage.SessionDatabase, *input.Storage.DataDirectory, "sessions.sqlite")
	fillPath(&input.Storage.WorkspaceDatabase, *input.Storage.DataDirectory, "workspaces.sqlite")
	fillPath(&input.Settings.Database, *input.Storage.DataDirectory, "settings.sqlite")
	sessionDirectory := filepath.Dir(*input.Storage.SessionDatabase)
	fillPath(&input.Storage.QueryDatabase, sessionDirectory, "query.sqlite")
	fillPath(&input.Storage.CacheDatabase, sessionDirectory, "projections.sqlite")
	fillPath(&input.Storage.CredentialsPath, sessionDirectory, ".credentials.json")
	return nil
}

func fillPath(target **string, directory string, name string) {
	if *target == nil {
		value := filepath.Join(directory, name)
		*target = &value
	}
}
