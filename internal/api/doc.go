// Package api is the REST transport boundary (Phase 12), served under
// /api/v1.
//
// It translates HTTP requests into application-service calls and maps the
// results to JSON. It contains no workflow execution logic: starting an
// execution persists it PENDING and enqueues it; workers run it.
package api
