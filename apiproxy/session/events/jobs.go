package sessionevents

import (
	"errors"
	"fmt"
)

// JobID identifies one process-local background job.
type JobID string

// JobStatus is the browser-visible background job lifecycle state.
type JobStatus string

const (
	JobRunning   JobStatus = "running"
	JobStopping  JobStatus = "stopping"
	JobCompleted JobStatus = "completed"
	JobKilled    JobStatus = "killed"
	JobFailed    JobStatus = "failed"
)

// Job is the browser-safe subset of a background job record.
type Job struct {
	ID         JobID     `json:"id"`
	Kind       string    `json:"kind"`
	Label      string    `json:"label"`
	Status     JobStatus `json:"status"`
	Detail     *string   `json:"detail,omitempty"`
	StartedAt  int64     `json:"startedAt"`
	FinishedAt *int64    `json:"finishedAt,omitempty"`
}

func validateJobs(tasks []Job) error {
	if tasks == nil {
		return errors.New("jobs must be an array")
	}
	for index, task := range tasks {
		if task.ID == "" || task.Kind == "" || task.Label == "" {
			return fmt.Errorf("job %d id, kind, and label must be non-empty", index)
		}
		if task.Status != JobRunning && task.Status != JobStopping && task.Status != JobCompleted && task.Status != JobKilled && task.Status != JobFailed {
			return fmt.Errorf("job %d status is invalid", index)
		}
		if task.StartedAt < 0 || (task.FinishedAt != nil && *task.FinishedAt < 0) {
			return fmt.Errorf("job %d timestamps must be non-negative", index)
		}
	}
	return nil
}
