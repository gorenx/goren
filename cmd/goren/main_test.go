package main

import (
	"path/filepath"
	"testing"
)

func TestCommandConfigResolvesOneDataDirectoryWithExplicitDatabaseOverrides(t *testing.T) {
	defaultDirectory := t.TempDir()
	configuredDirectory := filepath.Join(t.TempDir(), "configured")
	overriddenSession := filepath.Join(t.TempDir(), "custom-sessions.sqlite")
	overriddenWorkspace := filepath.Join(t.TempDir(), "custom-workspaces.sqlite")

	testCases := []struct {
		name          string
		settings      commandConfig
		wantDirectory string
		wantSession   string
		wantWorkspace string
	}{
		{
			name:          "default directory",
			wantDirectory: defaultDirectory,
			wantSession:   filepath.Join(defaultDirectory, "sessions.sqlite"),
			wantWorkspace: filepath.Join(defaultDirectory, "workspaces.sqlite"),
		},
		{
			name:          "configured directory",
			settings:      commandConfig{dataDirectory: configuredDirectory},
			wantDirectory: configuredDirectory,
			wantSession:   filepath.Join(configuredDirectory, "sessions.sqlite"),
			wantWorkspace: filepath.Join(configuredDirectory, "workspaces.sqlite"),
		},
		{
			name: "database overrides",
			settings: commandConfig{
				dataDirectory: configuredDirectory, sessionDatabase: overriddenSession,
				workspaceDatabase: overriddenWorkspace,
			},
			wantDirectory: configuredDirectory,
			wantSession:   overriddenSession,
			wantWorkspace: overriddenWorkspace,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			paths, err := testCase.settings.resolveStorage(defaultDirectory)
			if err != nil {
				t.Fatal(err)
			}
			if paths.dataDirectory != testCase.wantDirectory || paths.sessionDatabase != testCase.wantSession ||
				paths.workspaceDatabase != testCase.wantWorkspace {
				t.Fatalf("storage paths = %#v, want (%q, %q, %q)", paths,
					testCase.wantDirectory, testCase.wantSession, testCase.wantWorkspace)
			}
		})
	}
}
