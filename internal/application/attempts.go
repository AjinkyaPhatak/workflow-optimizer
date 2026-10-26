package application

import (
	"errors"
	"sync"
	"time"
)

// ErrTooManyAttempts: a sign-in (or password check) failed too often
// recently; the account is locked for a while. It stops guessing passwords.
var ErrTooManyAttempts = errors.New("too many attempts")

// Sign-in attempt limits: after MaxFailedAttempts wrong passwords within
// AttemptWindow, the key is locked until AttemptWindow after the first of
// them.
const (
	MaxFailedAttempts = 10
	AttemptWindow     = 15 * time.Minute
	// maxTrackedKeys bounds the memory the guard uses; beyond it, expired
	// entries are dropped first.
	maxTrackedKeys = 10000
)

// attemptGuard counts failed password checks per key (a normalized email,
// or a user ID for password changes). It is in memory: each API process
// counts its own attempts, and a restart forgets them.
type attemptGuard struct {
	mu    sync.Mutex
	now   func() time.Time
	fails map[string]*attempts
}

type attempts struct {
	first time.Time
	count int
}

func newAttemptGuard() *attemptGuard {
	return &attemptGuard{now: time.Now, fails: map[string]*attempts{}}
}

// locked reports whether key has reached the limit within the window.
func (g *attemptGuard) locked(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	a, ok := g.fails[key]
	if !ok {
		return false
	}
	if g.now().Sub(a.first) >= AttemptWindow {
		delete(g.fails, key)
		return false
	}
	return a.count >= MaxFailedAttempts
}

// fail records a failed check.
func (g *attemptGuard) fail(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	a, ok := g.fails[key]
	if !ok || now.Sub(a.first) >= AttemptWindow {
		if len(g.fails) >= maxTrackedKeys {
			g.prune(now)
		}
		g.fails[key] = &attempts{first: now, count: 1}
		return
	}
	a.count++
}

// reset forgets key's failures (after a correct password).
func (g *attemptGuard) reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, key)
}

// prune drops expired entries, and if none expired, the oldest half.
func (g *attemptGuard) prune(now time.Time) {
	for k, a := range g.fails {
		if now.Sub(a.first) >= AttemptWindow {
			delete(g.fails, k)
		}
	}
	if len(g.fails) < maxTrackedKeys {
		return
	}
	n := 0
	for k := range g.fails {
		if n >= maxTrackedKeys/2 {
			break
		}
		delete(g.fails, k)
		n++
	}
}
