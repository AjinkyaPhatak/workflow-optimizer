// Package config provides the environment-based configuration boundary.
package config

import "os"

// Config contains configuration needed by the architectural entrypoints.
type Config struct {
	DatabaseURL string
	RedisURL    string
	OpenAIAPIKey string
}

// Load reads the small Phase 1 configuration surface from the environment.
func Load() Config {
	return Config{
		DatabaseURL:  os.Getenv("DATABASE_URL"),
		RedisURL:     os.Getenv("REDIS_URL"),
		OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
	}
}
