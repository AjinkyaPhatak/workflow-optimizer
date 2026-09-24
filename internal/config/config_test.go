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
