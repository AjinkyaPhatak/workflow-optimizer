// Package auth owns the authentication boundary: user records, password
// hashing, and bearer tokens (Phase 12). HTTP handlers never parse tokens
// themselves; they go through TokenService.
package auth
