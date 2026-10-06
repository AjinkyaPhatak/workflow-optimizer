// Package credential owns workspace credentials: the domain model, the
// encryption and persistence boundaries, and the Service that creates,
// lists, deletes and resolves credentials (decrypting secrets into runtime
// memory only, inside the caller's workspace).
package credential
