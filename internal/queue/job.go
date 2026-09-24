// Package queue owns the Phase 9 job-queue contracts: the Job carried to
// workers, the JobQueue abstraction, and dispatching executions onto it.
//
// Queue semantics are at-least-once oriented: a job may be delivered more
// than once, and duplicates are harmless because the PostgreSQL claim
// (execution PENDING -> RUNNING, Phase 8) decides which worker owns an
// execution. The queue only ever says "try this execution".
//
// The package has no Redis or PostgreSQL dependency; the Redis implementation
// lives in internal/infrastructure/redis.
package queue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// JobSchemaVersion is the wire-format version of an encoded Job.
const JobSchemaVersion = 1

// maxPayloadBytes bounds decoding work for a hostile or corrupted payload.
// An encoded Job is ~70 bytes.
const maxPayloadBytes = 1024

var (
	// ErrInvalidJob rejects a Job that must not be enqueued.
	ErrInvalidJob = errors.New("queue: invalid job")
	// ErrMalformedJob marks a dequeued payload that is not a valid Job. The
	// payload has already been removed from the queue.
	ErrMalformedJob = errors.New("queue: malformed job payload")
)

// Job asks a worker to try one execution. It carries only the execution's
// identity: the workflow definition, input, credentials and state stay in
// PostgreSQL and are loaded by the worker.
type Job struct {
	ExecutionID uuid.UUID
}

// Validate reports whether the job can be enqueued.
func (j Job) Validate() error {
	if j.ExecutionID == uuid.Nil {
		return fmt.Errorf("%w: execution ID is required", ErrInvalidJob)
	}
	return nil
}

// wireJob is the serialized form. Field order is fixed, so encoding is
// deterministic.
type wireJob struct {
	Version     int    `json:"v"`
	ExecutionID string `json:"execution_id"`
}

// EncodeJob serializes a valid job.
func EncodeJob(j Job) ([]byte, error) {
	if err := j.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(wireJob{Version: JobSchemaVersion, ExecutionID: j.ExecutionID.String()})
}

// MalformedJobError carries the rejected payload for diagnostics.
type MalformedJobError struct {
	Payload []byte
	Reason  string
}

func (e *MalformedJobError) Error() string {
	shown := e.Payload
	if len(shown) > 128 {
		shown = shown[:128]
	}
	return fmt.Sprintf("%v: %s (payload %q)", ErrMalformedJob, e.Reason, shown)
}

// Is matches ErrMalformedJob.
func (e *MalformedJobError) Is(target error) bool { return target == ErrMalformedJob }

// DecodeJob strictly parses a payload: exactly one JSON object with a known
// schema version, no unknown fields, and a non-nil canonical execution UUID.
// Any other input is a *MalformedJobError; it never panics.
func DecodeJob(payload []byte) (Job, error) {
	malformed := func(reason string) (Job, error) {
		return Job{}, &MalformedJobError{Payload: append([]byte(nil), payload...), Reason: reason}
	}
	if len(payload) == 0 {
		return malformed("empty payload")
	}
	if len(payload) > maxPayloadBytes {
		return malformed(fmt.Sprintf("payload is %d bytes, limit %d", len(payload), maxPayloadBytes))
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	var w wireJob
	if err := dec.Decode(&w); err != nil {
		return malformed("invalid JSON: " + err.Error())
	}
	if dec.More() {
		return malformed("trailing data after job object")
	}
	if w.Version != JobSchemaVersion {
		return malformed(fmt.Sprintf("unsupported job schema version %d", w.Version))
	}
	id, err := uuid.Parse(w.ExecutionID)
	if err != nil || id.String() != w.ExecutionID {
		return malformed(fmt.Sprintf("execution_id %q is not a canonical UUID", w.ExecutionID))
	}
	if id == uuid.Nil {
		return malformed("execution_id is the nil UUID")
	}
	return Job{ExecutionID: id}, nil
}
