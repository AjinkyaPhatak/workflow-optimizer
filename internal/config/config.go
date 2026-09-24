// Package config provides the environment-based configuration boundary.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults for the Phase 9 worker system.
const (
	// DefaultRedisQueueName is the single, centralized name of the execution
	// job queue (a Redis list).
	DefaultRedisQueueName = "workflow:executions"
	// DefaultWorkerCount is the number of workers a worker process runs.
	DefaultWorkerCount = 1
	// DefaultWorkerShutdownTimeout bounds how long a worker process waits for
	// in-flight jobs to observe cancellation during graceful shutdown.
	DefaultWorkerShutdownTimeout = 30 * time.Second
)

// ErrInvalidConfig marks a configuration value that is present but unusable,
// or missing where it is required.
var ErrInvalidConfig = errors.New("config: invalid configuration")

// Config contains configuration needed by the architectural entrypoints.
type Config struct {
	DatabaseURL  string
	RedisURL     string
	OpenAIAPIKey string

	// RedisQueueName is the Redis list that carries execution jobs
	// (REDIS_QUEUE_NAME, default DefaultRedisQueueName).
	RedisQueueName string
	// WorkerCount is the number of concurrent workers in a worker process
	// (WORKER_COUNT, default DefaultWorkerCount).
	WorkerCount int
	// WorkerShutdownTimeout bounds graceful shutdown
	// (WORKER_SHUTDOWN_TIMEOUT, a Go duration such as "30s").
	WorkerShutdownTimeout time.Duration
}

// Load reads configuration from the environment. Unset optional values get
// defaults; values that are set but malformed are reported, never replaced
// by a default.
func Load() (Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom reads configuration through getenv (used by Load and by tests).
func LoadFrom(getenv func(string) string) (Config, error) {
	cfg := Config{
		DatabaseURL:           getenv("DATABASE_URL"),
		RedisURL:              getenv("REDIS_URL"),
		OpenAIAPIKey:          getenv("OPENAI_API_KEY"),
		RedisQueueName:        DefaultRedisQueueName,
		WorkerCount:           DefaultWorkerCount,
		WorkerShutdownTimeout: DefaultWorkerShutdownTimeout,
	}
	var problems []string
	if v, ok := lookup(getenv, "REDIS_QUEUE_NAME"); ok {
		cfg.RedisQueueName = v
	}
	if v, ok := lookup(getenv, "WORKER_COUNT"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("WORKER_COUNT %q is not an integer", v))
		} else {
			cfg.WorkerCount = n
		}
	}
	if v, ok := lookup(getenv, "WORKER_SHUTDOWN_TIMEOUT"); ok {
		d, err := time.ParseDuration(v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("WORKER_SHUTDOWN_TIMEOUT %q is not a duration (e.g. \"30s\")", v))
		} else {
			cfg.WorkerShutdownTimeout = d
		}
	}
	if len(problems) > 0 {
		return cfg, fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return cfg, nil
}

// ValidateWorker checks everything a worker process needs.
func (c Config) ValidateWorker() error {
	var problems []string
	if strings.TrimSpace(c.DatabaseURL) == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	if strings.TrimSpace(c.RedisURL) == "" {
		problems = append(problems, "REDIS_URL is required")
	}
	if strings.TrimSpace(c.RedisQueueName) == "" {
		problems = append(problems, "REDIS_QUEUE_NAME must not be empty")
	}
	if c.WorkerCount < 1 {
		problems = append(problems, fmt.Sprintf("WORKER_COUNT must be at least 1, got %d", c.WorkerCount))
	}
	if c.WorkerShutdownTimeout <= 0 {
		problems = append(problems, fmt.Sprintf("WORKER_SHUTDOWN_TIMEOUT must be positive, got %s", c.WorkerShutdownTimeout))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}

// lookup returns a trimmed, non-empty environment value.
func lookup(getenv func(string) string, key string) (string, bool) {
	v := strings.TrimSpace(getenv(key))
	return v, v != ""
}
