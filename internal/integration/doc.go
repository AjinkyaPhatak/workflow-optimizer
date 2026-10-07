// Package integration is the framework third-party integrations (Gmail,
// Google Docs, Discord, ...) are built on (Phase C1). It adds no execution
// mechanism: every integration action is an ordinary workflow node.
//
//	Integration (metadata: name, auth, actions)
//	    └── Action  -> node type "<integration>.<action>" + node.NodeDefinition
//	                -> ActionNode (node.Node) in the node registry
//	                       credential_id -> credential.Resolver -> ResolvedCredential
//	                       -> Connector -> provider-specific client -> external API
//
// Two registries, two jobs:
//
//   - the integration Registry is discovery metadata only (which integrations
//     and actions exist);
//   - the node registry (internal/node) stays the only registry of
//     executable implementations. Install registers both, from one Module.
//
// The graph executor knows nothing about integrations. Integrations classify
// their failures (Error, Kind); the Phase 10 engine decides about retries,
// together with the action's side-effect declaration.
//
// Workflow definitions only ever hold a credential's ID. Secrets exist in
// runtime memory, inside ActionNode.Execute and the client it connects.
package integration
