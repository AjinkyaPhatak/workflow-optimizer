package reliability

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
)

// DeadLetterNotice announces a dead-lettered execution. It is informational
// only: the authoritative record (and its reason) is the
// execution_dead_letters row written with the FAILED transition. A notice
// never re-enqueues anything: dead-lettered executions are terminal.
type DeadLetterNotice struct {
	ExecutionID uuid.UUID                  `json:"execution_id"`
	Attempt     int                        `json:"attempt"`
	Reason      execution.DeadLetterReason `json:"reason"`
	Code        string                     `json:"code,omitempty"`
}

// Pusher appends a payload to a list (the Redis dead-letter list).
type Pusher interface {
	Push(ctx context.Context, payload []byte) error
}

// DeadLetterNotifier publishes notices; failures are logged, never fatal,
// because the database already holds the authoritative record.
func DeadLetterNotifier(p Pusher, logger *slog.Logger) func(context.Context, DeadLetterNotice) {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, n DeadLetterNotice) {
		payload, err := json.Marshal(struct {
			V int `json:"v"`
			DeadLetterNotice
		}{1, n})
		if err == nil {
			err = p.Push(ctx, payload)
		}
		if err != nil {
			logger.Warn("could not publish dead-letter notice (the database record is authoritative)",
				"execution_id", n.ExecutionID, "error", err)
		}
	}
}
