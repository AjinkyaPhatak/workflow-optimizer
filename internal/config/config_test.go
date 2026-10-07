package config_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"workflow-optimizer/internal/config"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaultsWhenUnset(t *testing.T) {
	cfg, err := config.LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RedisQueueName != "workflow:executions" || cfg.WorkerCount != 1 || cfg.WorkerShutdownTimeout != 30*time.Second {
		t.Fatalf("defaults = %+v", cfg)
	}
}

func TestExplicitValues(t *testing.T) {
	cfg, err := config.LoadFrom(env(map[string]string{
		"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://h:1/0",
		"REDIS_QUEUE_NAME": "custom:q", "WORKER_COUNT": " 5 ", "WORKER_SHUTDOWN_TIMEOUT": "90s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RedisQueueName != "custom:q" || cfg.WorkerCount != 5 || cfg.WorkerShutdownTimeout != 90*time.Second || cfg.RedisURL != "redis://h:1/0" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if err := cfg.ValidateWorker(); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedValuesAreReportedNotDefaulted(t *testing.T) {
	_, err := config.LoadFrom(env(map[string]string{"WORKER_COUNT": "five", "WORKER_SHUTDOWN_TIMEOUT": "10"}))
	if !errors.Is(err, config.ErrInvalidConfig) || !strings.Contains(err.Error(), "WORKER_COUNT") || !strings.Contains(err.Error(), "WORKER_SHUTDOWN_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateWorker(t *testing.T) {
	base := config.Config{DatabaseURL: "postgres://x", RedisURL: "redis://x", RedisQueueName: "q", WorkerCount: 1, WorkerShutdownTimeout: time.Second}
	cases := map[string]func(*config.Config){
		"no database":   func(c *config.Config) { c.DatabaseURL = "" },
		"no redis":      func(c *config.Config) { c.RedisURL = " " },
		"no queue name": func(c *config.Config) { c.RedisQueueName = "" },
		"zero workers":  func(c *config.Config) { c.WorkerCount = 0 },
		"no timeout":    func(c *config.Config) { c.WorkerShutdownTimeout = 0 },
	}
	for name, mutate := range cases {
		c := base
		mutate(&c)
		if err := c.ValidateWorker(); !errors.Is(err, config.ErrInvalidConfig) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestReliabilityDefaultsAndOverrides(t *testing.T) {
	cfg, err := config.LoadFrom(env(nil))
	if err != nil || cfg.Reliability != config.DefaultReliability() {
		t.Fatalf("defaults = %+v, %v", cfg.Reliability, err)
	}
	r := cfg.Reliability
	if r.MaxAttempts != 3 || r.InitialRetryDelay != time.Second || r.MaxRetryDelay != 5*time.Minute || r.BackoffMultiplier != 2 ||
		r.ExecutionTimeout != time.Hour || r.NodeTimeout != 5*time.Minute || r.NodeMaxAttempts != 2 || r.ReaperInterval != 15*time.Second ||
		r.WorkerLeaseDuration != 30*time.Second || r.WorkerHeartbeatInterval != 10*time.Second || r.SchedulerInterval != time.Second ||
		r.DeadLetterQueue != "workflow:dead-letter" {
		t.Fatalf("defaults = %+v", r)
	}
	cfg, err = config.LoadFrom(env(map[string]string{
		"DEFAULT_MAX_ATTEMPTS": "5", "DEFAULT_INITIAL_RETRY_DELAY": "250ms", "DEFAULT_MAX_RETRY_DELAY": "1m",
		"DEFAULT_BACKOFF_MULTIPLIER": "3", "DEFAULT_EXECUTION_TIMEOUT": "10m", "DEFAULT_NODE_TIMEOUT": "30s",
		"DEFAULT_NODE_MAX_ATTEMPTS": "1", "REAPER_INTERVAL": "5s", "WORKER_LEASE_DURATION": "20s",
		"WORKER_HEARTBEAT_INTERVAL": "5s", "SCHEDULER_INTERVAL": "500ms", "REDIS_DEAD_LETTER_QUEUE": "dlq",
	}))
	r = cfg.Reliability
	if err != nil || r.MaxAttempts != 5 || r.InitialRetryDelay != 250*time.Millisecond || r.MaxRetryDelay != time.Minute ||
		r.BackoffMultiplier != 3 || r.ExecutionTimeout != 10*time.Minute || r.NodeTimeout != 30*time.Second || r.NodeMaxAttempts != 1 ||
		r.ReaperInterval != 5*time.Second || r.WorkerLeaseDuration != 20*time.Second || r.WorkerHeartbeatInterval != 5*time.Second ||
		r.SchedulerInterval != 500*time.Millisecond || r.DeadLetterQueue != "dlq" {
		t.Fatalf("overrides = %+v, %v", r, err)
	}
	if err := cfg.ValidateReliability(); err != nil {
		t.Fatal(err)
	}
}

func TestReliabilityMalformedAndInvalidValues(t *testing.T) {
	_, err := config.LoadFrom(env(map[string]string{"DEFAULT_MAX_ATTEMPTS": "three", "DEFAULT_BACKOFF_MULTIPLIER": "x", "REAPER_INTERVAL": "5"}))
	if !errors.Is(err, config.ErrInvalidConfig) || !strings.Contains(err.Error(), "DEFAULT_MAX_ATTEMPTS") ||
		!strings.Contains(err.Error(), "DEFAULT_BACKOFF_MULTIPLIER") || !strings.Contains(err.Error(), "REAPER_INTERVAL") {
		t.Fatalf("err = %v", err)
	}
	invalid := map[string]map[string]string{
		"zero attempts":          {"DEFAULT_MAX_ATTEMPTS": "0"},
		"max below initial":      {"DEFAULT_INITIAL_RETRY_DELAY": "10s", "DEFAULT_MAX_RETRY_DELAY": "1s"},
		"shrinking backoff":      {"DEFAULT_BACKOFF_MULTIPLIER": "0.5"},
		"lease < 2x heartbeat":   {"WORKER_LEASE_DURATION": "15s", "WORKER_HEARTBEAT_INTERVAL": "10s"},
		"zero timeout":           {"DEFAULT_EXECUTION_TIMEOUT": "0s"},
		"dlq is the job queue":   {"REDIS_DEAD_LETTER_QUEUE": "workflow:executions"},
		"zero node attempts":     {"DEFAULT_NODE_MAX_ATTEMPTS": "0"},
		"negative scheduler gap": {"SCHEDULER_INTERVAL": "-1s"},
	}
	for name, vars := range invalid {
		cfg, err := config.LoadFrom(env(vars))
		if err != nil {
			t.Fatalf("%s: load: %v", name, err)
		}
		if err := cfg.ValidateReliability(); !errors.Is(err, config.ErrInvalidConfig) {
			t.Errorf("%s accepted", name)
		}
	}
	// An unconfigured (zero) block means defaults; an explicit bad value is
	// never silently replaced by a default.
	if err := (config.Config{RedisQueueName: "q"}).ValidateReliability(); err != nil {
		t.Fatalf("zero block: %v", err)
	}
	bad := config.Config{RedisQueueName: "q", Reliability: config.Reliability{MaxAttempts: 0, NodeMaxAttempts: 1}}
	if err := bad.ValidateReliability(); err == nil {
		t.Fatal("partially configured reliability with zero attempts was accepted")
	}
}

func TestAPIConfig(t *testing.T) {
	cfg, err := config.LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIAddr != ":8080" || cfg.AuthTokenTTL != 24*time.Hour || cfg.AuthTokenSecret != "" {
		t.Fatalf("API defaults = %q %s", cfg.APIAddr, cfg.AuthTokenTTL)
	}
	if err := cfg.ValidateAPI(); err == nil || !strings.Contains(err.Error(), "AUTH_TOKEN_SECRET") {
		t.Fatalf("missing secret accepted: %v", err)
	}
	secret := strings.Repeat("s", 32)
	cfg, err = config.LoadFrom(env(map[string]string{"DATABASE_URL": "postgres://x", "REDIS_URL": "redis://x",
		"API_ADDR": "127.0.0.1:9000", "AUTH_TOKEN_SECRET": secret, "AUTH_TOKEN_TTL": "1h"}))
	if err != nil || cfg.APIAddr != "127.0.0.1:9000" || cfg.AuthTokenTTL != time.Hour || cfg.AuthTokenSecret != secret {
		t.Fatalf("explicit = %+v %v", cfg, err)
	}
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatal(err)
	}
	cfg.AuthTokenSecret = "short"
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("short secret accepted")
	}
	if _, err := config.LoadFrom(env(map[string]string{"AUTH_TOKEN_TTL": "soon"})); err == nil {
		t.Fatal("malformed AUTH_TOKEN_TTL accepted")
	}
}

func TestObservabilityConfig(t *testing.T) {
	cfg, err := config.LoadFrom(env(nil))
	if err != nil || !cfg.ExposeNodeData || cfg.ModelPricing != "" {
		t.Fatalf("defaults = %v %q %v", cfg.ExposeNodeData, cfg.ModelPricing, err)
	}
	cfg, err = config.LoadFrom(env(map[string]string{"OBSERVABILITY_EXPOSE_NODE_DATA": "false", "MODEL_PRICING": `{"m":{"input_per_million":1}}`}))
	if err != nil || cfg.ExposeNodeData || cfg.ModelPricing == "" {
		t.Fatalf("explicit = %v %q %v", cfg.ExposeNodeData, cfg.ModelPricing, err)
	}
	if _, err := config.LoadFrom(env(map[string]string{"OBSERVABILITY_EXPOSE_NODE_DATA": "sometimes"})); err == nil {
		t.Fatal("malformed OBSERVABILITY_EXPOSE_NODE_DATA accepted")
	}
}
