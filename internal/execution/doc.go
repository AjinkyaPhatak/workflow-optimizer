// Package execution owns workflow execution. Phase 7 provides the in-memory,
// sequential GraphExecutor: planner.go orders a validated DAG, resolver.go maps
// upstream outputs onto node inputs, variables.go resolves {{references}} in
// node configuration, and executor.go dispatches nodes through node.Registry.
// Durable execution records (persistence.go) and lifecycle state belong to
// later phases. The package depends on node and workflow contracts, not on
// transport, infrastructure, or specific node implementations.
package execution
