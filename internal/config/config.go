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

	// Phase 10 reliability defaults.
	DefaultMaxAttempts             = 3
	DefaultInitialRetryDelay       = time.Second
	DefaultMaxRetryDelay           = 5 * time.Minute
	DefaultBackoffMultiplier       = 2.0
	DefaultExecutionTimeout        = time.Hour
	DefaultNodeTimeout             = 5 * time.Minute
	DefaultNodeMaxAttempts         = 2
	DefaultReaperInterval          = 15 * time.Second
	DefaultWorkerLeaseDuration     = 30 * time.Second
	DefaultWorkerHeartbeatInterval = 10 * time.Second
	DefaultSchedulerInterval       = time.Second
	// DefaultRedisDeadLetterQueue is the Redis list that receives
	// (non-authoritative) dead-letter notices.
	DefaultRedisDeadLetterQueue = "workflow:dead-letter"

	// Phase 12 API defaults.
	DefaultAPIAddr      = ":8080"
	DefaultAuthTokenTTL = 24 * time.Hour
	// MinAuthTokenSecretLength is the minimum AUTH_TOKEN_SECRET size (bytes).
	MinAuthTokenSecretLength = 32
)

// ErrInvalidConfig marks a configuration value that is present but unusable,
// or missing where it is required.
var ErrInvalidConfig = errors.New("config: invalid configuration")

// Config contains configuration needed by the architectural entrypoints.
type Config struct {
	DatabaseURL string
	RedisURL    string

	// CredentialEncryptionKey is the base64 AES-256 master key that encrypts
	// workspace credentials at rest (CREDENTIAL_ENCRYPTION_KEY, 32 bytes;
	// never committed). Without it credentials can be neither created nor
	// resolved. Provider API keys are never configured here: they are
	// workspace credentials (Phase 11).
	CredentialEncryptionKey string
	// OpenAIBaseURL overrides the OpenAI API endpoint (OPENAI_BASE_URL,
	// default https://api.openai.com/v1).
	OpenAIBaseURL string

	// RedisQueueName is the Redis list that carries execution jobs
	// (REDIS_QUEUE_NAME, default DefaultRedisQueueName).
	RedisQueueName string
	// WorkerCount is the number of concurrent workers in a worker process
	// (WORKER_COUNT, default DefaultWorkerCount).
	WorkerCount int
	// WorkerShutdownTimeout bounds graceful shutdown
	// (WORKER_SHUTDOWN_TIMEOUT, a Go duration such as "30s").
	WorkerShutdownTimeout time.Duration

	// Reliability (Phase 10).
	Reliability Reliability

	// API (Phase 12).

	// APIAddr is the API server's listen address (API_ADDR).
	APIAddr string
	// AuthTokenSecret signs bearer tokens (AUTH_TOKEN_SECRET, at least 32
	// bytes; never committed).
	AuthTokenSecret string
	// AuthTokenTTL is the lifetime of issued tokens (AUTH_TOKEN_TTL).
	AuthTokenTTL time.Duration
}

// Reliability is the Phase 10 retry, timeout and recovery configuration.
type Reliability struct {
	// Retry policy of executions (DEFAULT_MAX_ATTEMPTS includes the first
	// attempt; 1 disables retries).
	MaxAttempts       int           // DEFAULT_MAX_ATTEMPTS
	InitialRetryDelay time.Duration // DEFAULT_INITIAL_RETRY_DELAY
	MaxRetryDelay     time.Duration // DEFAULT_MAX_RETRY_DELAY
	BackoffMultiplier float64       // DEFAULT_BACKOFF_MULTIPLIER
	// ExecutionTimeout is the total budget of an execution, from its first
	// claim, across all attempts (DEFAULT_EXECUTION_TIMEOUT).
	ExecutionTimeout time.Duration
	// NodeTimeout bounds one node invocation (DEFAULT_NODE_TIMEOUT).
	NodeTimeout time.Duration
	// NodeMaxAttempts bounds in-place invocations of a failing node within
	// one execution attempt (DEFAULT_NODE_MAX_ATTEMPTS; 1 disables them).
	NodeMaxAttempts int
	// Recovery.
	ReaperInterval          time.Duration // REAPER_INTERVAL
	WorkerLeaseDuration     time.Duration // WORKER_LEASE_DURATION
	WorkerHeartbeatInterval time.Duration // WORKER_HEARTBEAT_INTERVAL
	SchedulerInterval       time.Duration // SCHEDULER_INTERVAL
	// DeadLetterQueue is the Redis list for dead-letter notices
	// (REDIS_DEAD_LETTER_QUEUE).
	DeadLetterQueue string
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
		DatabaseURL:             getenv("DATABASE_URL"),
		RedisURL:                getenv("REDIS_URL"),
		CredentialEncryptionKey: getenv("CREDENTIAL_ENCRYPTION_KEY"),
		OpenAIBaseURL:           getenv("OPENAI_BASE_URL"),
		RedisQueueName:          DefaultRedisQueueName,
		WorkerCount:             DefaultWorkerCount,
		WorkerShutdownTimeout:   DefaultWorkerShutdownTimeout,
		Reliability:             DefaultReliability(),
		APIAddr:                 DefaultAPIAddr,
		AuthTokenSecret:         getenv("AUTH_TOKEN_SECRET"),
		AuthTokenTTL:            DefaultAuthTokenTTL,
	}
	var problems []string
	readInt := func(key string, dst *int) {
		if v, ok := lookup(getenv, key); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s %q is not an integer", key, v))
				return
			}
			*dst = n
		}
	}
	readDuration := func(key string, dst *time.Duration) {
		if v, ok := lookup(getenv, key); ok {
			d, err := time.ParseDuration(v)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s %q is not a duration (e.g. \"30s\")", key, v))
				return
			}
			*dst = d
		}
	}
	rel := &cfg.Reliability
	readInt("DEFAULT_MAX_ATTEMPTS", &rel.MaxAttempts)
	readDuration("DEFAULT_INITIAL_RETRY_DELAY", &rel.InitialRetryDelay)
	readDuration("DEFAULT_MAX_RETRY_DELAY", &rel.MaxRetryDelay)
	if v, ok := lookup(getenv, "DEFAULT_BACKOFF_MULTIPLIER"); ok {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			problems = append(problems, fmt.Sprintf("DEFAULT_BACKOFF_MULTIPLIER %q is not a number", v))
		} else {
			rel.BackoffMultiplier = f
		}
	}
	readDuration("DEFAULT_EXECUTION_TIMEOUT", &rel.ExecutionTimeout)
	readDuration("DEFAULT_NODE_TIMEOUT", &rel.NodeTimeout)
	readInt("DEFAULT_NODE_MAX_ATTEMPTS", &rel.NodeMaxAttempts)
	readDuration("REAPER_INTERVAL", &rel.ReaperInterval)
	readDuration("WORKER_LEASE_DURATION", &rel.WorkerLeaseDuration)
	readDuration("WORKER_HEARTBEAT_INTERVAL", &rel.WorkerHeartbeatInterval)
	readDuration("SCHEDULER_INTERVAL", &rel.SchedulerInterval)
	if v, ok := lookup(getenv, "REDIS_DEAD_LETTER_QUEUE"); ok {
		rel.DeadLetterQueue = v
	}
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
	if v, ok := lookup(getenv, "API_ADDR"); ok {
		cfg.APIAddr = v
	}
	readDuration("AUTH_TOKEN_TTL", &cfg.AuthTokenTTL)
	if len(problems) > 0 {
		return cfg, fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return cfg, nil
}

// ValidateAPI checks everything an API process needs.
func (c Config) ValidateAPI() error {
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
	if strings.TrimSpace(c.APIAddr) == "" {
		problems = append(problems, "API_ADDR must not be empty")
	}
	if len(c.AuthTokenSecret) < MinAuthTokenSecretLength {
		problems = append(problems, fmt.Sprintf("AUTH_TOKEN_SECRET is required and must be at least %d bytes", MinAuthTokenSecretLength))
	}
	if c.AuthTokenTTL <= 0 {
		problems = append(problems, fmt.Sprintf("AUTH_TOKEN_TTL must be positive, got %s", c.AuthTokenTTL))
	}
	problems = append(problems, c.Reliability.OrDefaults().problems(c.RedisQueueName)...)
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
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
	problems = append(problems, c.Reliability.OrDefaults().problems(c.RedisQueueName)...)
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

// ValidateReliability checks the Phase 10 settings on their own.
func (c Config) ValidateReliability() error {
	if problems := c.Reliability.OrDefaults().problems(c.RedisQueueName); len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(problems, "; "))
	}
	return nil
}

// DefaultReliability returns the default Phase 10 settings.
func DefaultReliability() Reliability {
	return Reliability{
		MaxAttempts:             DefaultMaxAttempts,
		InitialRetryDelay:       DefaultInitialRetryDelay,
		MaxRetryDelay:           DefaultMaxRetryDelay,
		BackoffMultiplier:       DefaultBackoffMultiplier,
		ExecutionTimeout:        DefaultExecutionTimeout,
		NodeTimeout:             DefaultNodeTimeout,
		NodeMaxAttempts:         DefaultNodeMaxAttempts,
		ReaperInterval:          DefaultReaperInterval,
		WorkerLeaseDuration:     DefaultWorkerLeaseDuration,
		WorkerHeartbeatInterval: DefaultWorkerHeartbeatInterval,
		SchedulerInterval:       DefaultSchedulerInterval,
		DeadLetterQueue:         DefaultRedisDeadLetterQueue,
	}
}

// OrDefaults returns DefaultReliability when r was never configured (all
// zero, e.g. a Config literal); a partially set r is returned unchanged so
// that invalid explicit values are reported, never replaced.
func (r Reliability) OrDefaults() Reliability {
	if r == (Reliability{}) {
		return DefaultReliability()
	}
	return r
}

func (r Reliability) problems(jobQueue string) []string {
	var p []string
	positive := func(name string, d time.Duration) {
		if d <= 0 {
			p = append(p, fmt.Sprintf("%s must be positive, got %s", name, d))
		}
	}
	if r.MaxAttempts < 1 {
		p = append(p, fmt.Sprintf("DEFAULT_MAX_ATTEMPTS must be at least 1, got %d", r.MaxAttempts))
	}
	if r.NodeMaxAttempts < 1 {
		p = append(p, fmt.Sprintf("DEFAULT_NODE_MAX_ATTEMPTS must be at least 1, got %d", r.NodeMaxAttempts))
	}
	positive("DEFAULT_INITIAL_RETRY_DELAY", r.InitialRetryDelay)
	positive("DEFAULT_MAX_RETRY_DELAY", r.MaxRetryDelay)
	if r.MaxRetryDelay < r.InitialRetryDelay {
		p = append(p, fmt.Sprintf("DEFAULT_MAX_RETRY_DELAY %s is below DEFAULT_INITIAL_RETRY_DELAY %s", r.MaxRetryDelay, r.InitialRetryDelay))
	}
	if r.BackoffMultiplier < 1 {
		p = append(p, fmt.Sprintf("DEFAULT_BACKOFF_MULTIPLIER must be at least 1, got %v", r.BackoffMultiplier))
	}
	positive("DEFAULT_EXECUTION_TIMEOUT", r.ExecutionTimeout)
	positive("DEFAULT_NODE_TIMEOUT", r.NodeTimeout)
	positive("REAPER_INTERVAL", r.ReaperInterval)
	positive("WORKER_LEASE_DURATION", r.WorkerLeaseDuration)
	positive("WORKER_HEARTBEAT_INTERVAL", r.WorkerHeartbeatInterval)
	positive("SCHEDULER_INTERVAL", r.SchedulerInterval)
	if r.WorkerHeartbeatInterval > 0 && r.WorkerLeaseDuration < 2*r.WorkerHeartbeatInterval {
		p = append(p, fmt.Sprintf("WORKER_LEASE_DURATION %s must be at least twice WORKER_HEARTBEAT_INTERVAL %s", r.WorkerLeaseDuration, r.WorkerHeartbeatInterval))
	}
	if strings.TrimSpace(r.DeadLetterQueue) == "" {
		p = append(p, "REDIS_DEAD_LETTER_QUEUE must not be empty")
	} else if r.DeadLetterQueue == jobQueue {
		p = append(p, "REDIS_DEAD_LETTER_QUEUE must differ from REDIS_QUEUE_NAME (dead letters are never work)")
	}
	return p
}
