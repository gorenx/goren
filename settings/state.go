package settings

import (
	"errors"
	"maps"
	"strings"
)

// Section identifies the capability part of a named Group.
type Section uint8

const (
	ProviderSection Section = iota + 1
	PromptSection
	MCPSection
	ToolSection
	CompanionSection
	AgentSection
)

const maximumRevision int64 = 9_007_199_254_740_991

var (
	// ErrInvalid identifies an invalid typed configuration or mutation.
	ErrInvalid = errors.New("settings: invalid configuration")
	// ErrConflict means the editor or conditional commit used an obsolete version.
	ErrConflict = errors.New("settings: configuration changed; reload before saving")
	// ErrStorage reports a storage failure without exposing stored values or SQL.
	ErrStorage = errors.New("settings: storage operation failed")
	// ErrUncertain means persistence must be read before deciding whether to retry.
	ErrUncertain = errors.New("settings: commit outcome requires verification")
	// ErrApplication means the target was saved but is not fully applied.
	ErrApplication = errors.New("settings: saved configuration could not be applied")
)

// Group identifies a complete named configuration within one capability.
type Group struct {
	Section Section
	Name    string
}

// Valid rejects unknown capabilities and noncanonical names.
func (identifier Group) Valid() bool {
	if identifier.Name == "" || strings.TrimSpace(identifier.Name) != identifier.Name {
		return false
	}
	switch identifier.Section {
	case ProviderSection, PromptSection, MCPSection, ToolSection, CompanionSection, AgentSection:
		return true
	default:
		return false
	}
}

// State is the persistent user snapshot, including revisions of cleared overrides.
type State struct {
	Revision int64
	Versions map[Group]int64
	User     Directory
}

// Clone detaches all mutable values and versions.
func (stored State) Clone() State {
	stored.User = stored.User.Clone()
	stored.Versions = maps.Clone(stored.Versions)
	return stored
}

// Version returns the last submission version of one named group.
func (stored State) Version(identifier Group) int64 { return stored.Versions[identifier] }

// Validate checks revision identities and complete user values before restoration.
func (stored State) Validate() error {
	if stored.Revision < 0 || stored.Revision > maximumRevision {
		return ErrInvalid
	}
	for identifier, revision := range stored.Versions {
		if !identifier.Valid() ||
			revision <= 0 ||
			revision > stored.Revision {
			return ErrInvalid
		}
	}
	for _, identifier := range stored.User.Groups() {
		if stored.Version(identifier) == 0 {
			return ErrInvalid
		}
	}
	return stored.User.Validate()
}

// Application reports acceptance in this process, not changes to active Turns.
type Application struct {
	AppliedRevision int64
	PendingRestart  bool
	Error           string
}

// Snapshot is a detached query result with independently named application status.
type Snapshot struct {
	Base         Directory
	State        State
	Target       Directory
	Applications map[Group]Application
}

// Clone detaches all mutable configuration, version and application maps.
func (view Snapshot) Clone() Snapshot {
	view.Base = view.Base.Clone()
	view.State = view.State.Clone()
	view.Target = view.Target.Clone()
	view.Applications = maps.Clone(view.Applications)
	return view
}
