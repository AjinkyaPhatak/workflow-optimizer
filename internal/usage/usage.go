// Package usage is a workspace's usage report (Phase M): how many runs, how
// they ended, AI tokens used, app steps run, the busiest workflows and file
// storage, over a recent period.
package usage

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// MaxDays bounds a report's period.
const MaxDays = 90

// Report is a workspace's usage since Since.
type Report struct {
	Since     time.Time
	Runs      Runs
	Daily     []Day
	AI        AI
	AppSteps  int64
	Workflows []WorkflowRuns
	Files     Storage
	// LiveWorkflows counts the workspace's workflows (not deleted).
	LiveWorkflows int64
}

// Runs counts executions by how they ended.
type Runs struct {
	Total, Completed, Failed, Cancelled, Active int64
}

// Day is one day's runs (UTC).
type Day struct {
	Date                         string // YYYY-MM-DD
	Completed, Failed, Cancelled int64
}

// AI sums the AI steps (Ask AI, transcription) that finished.
type AI struct {
	Steps                                  int64
	InputTokens, OutputTokens, TotalTokens int64
}

// WorkflowRuns is one workflow's runs in the period.
type WorkflowRuns struct {
	WorkflowID uuid.UUID
	Name       string
	Runs       int64
	Failed     int64
	LastRunAt  time.Time
}

// Storage is the workspace's uploaded files.
type Storage struct {
	Count int64
	Bytes int64
}

// Repository computes reports.
type Repository interface {
	WorkspaceUsage(ctx context.Context, workspaceID uuid.UUID, since time.Time) (Report, error)
}
