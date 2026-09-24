// Package redis is the boundary for Redis queueing, events, and transient state.
// Redis is not a durable source of truth: it tells workers which execution to
// try; PostgreSQL decides which worker owns it.
package redis
