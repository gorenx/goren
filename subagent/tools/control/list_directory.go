package control

import (
	"context"
	"fmt"
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/session"
	"github.com/gorenx/goren/subagent"
)

// directoryLister interprets the existing descriptor directory without changing
// OneShot or Continuable creation and recovery behavior.
type directoryLister struct {
	directory subagent.ChildDirectory
	activity  activityReader
}

func (adapter *directoryLister) List(ctx context.Context, input Listing) ([]Entry, error) {
	entries := make([]Entry, 0)
	switch input.Scope {
	case "children":
		rows, listErr := adapter.directory.ListChildren(
			ctx,
			input.ParentSessionID,
		)
		if listErr != nil {
			return nil, listErr
		}
		for _, row := range rows {
			projected, included, projectErr := adapter.project(ctx, row, nil, nil)
			if projectErr != nil {
				return nil, projectErr
			}
			if included {
				entries = append(entries, projected)
			}
		}
	case "descendants":
		rows, listErr := adapter.directory.ListDescendants(
			ctx,
			input.ParentSessionID,
		)
		if listErr != nil {
			return nil, listErr
		}
		for _, row := range rows {
			parentID := string(row.ParentID)
			depth := row.Depth
			projected, included, projectErr := adapter.project(
				ctx,
				row.Entry,
				&parentID,
				&depth,
			)
			if projectErr != nil {
				return nil, projectErr
			}
			if included {
				entries = append(entries, projected)
			}
		}
	default:
		return nil, fmt.Errorf("list_agents: unsupported scope %q", input.Scope)
	}
	return entries, nil
}

func (adapter *directoryLister) project(
	requestContext context.Context,
	entry subagent.ChildEntry,
	parentID *string,
	depth *int64,
) (Entry, bool, error) {
	switch row := entry.(type) {
	case subagent.ContinuableChildEntry:
		status, err := adapter.status(requestContext, row.ID)
		return Entry{
			Kind:   "child",
			ID:     string(row.ID),
			Label:  row.Label,
			Status: status,
			Parent: parentID,
			Depth:  depth,
		}, true, err
	case subagent.BoundChildEntry:
		status, err := adapter.status(requestContext, row.ID)
		return Entry{
			Kind:   "child",
			ID:     string(row.ID),
			Label:  row.Label,
			Status: status,
			Parent: parentID,
			Depth:  depth,
		}, true, err
	case subagent.DiagnosticEntry:
		return Entry{
			Kind:   "diagnostic",
			ID:     string(row.ID),
			Reason: string(row.Reason),
			Parent: parentID,
			Depth:  depth,
		}, true, nil
	case subagent.OneShotChildEntry:
		return Entry{}, false, nil
	default:
		return Entry{}, false, nil
	}
}

func (adapter *directoryLister) status(requestContext context.Context, childID session.SessionID) (string, error) {
	status, found, err := adapter.activity.Status(requestContext, childID)
	if err != nil {
		return "", err
	}
	if !found {
		return "ready", nil
	}
	if status == agent.StatusRunning {
		return "running", nil
	}
	return "idle", nil
}
