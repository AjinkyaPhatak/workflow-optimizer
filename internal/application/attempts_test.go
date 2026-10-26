package application

import (
	"testing"
	"time"
)

func TestAttemptGuardLocksAfterRepeatedFailures(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	g := newAttemptGuard()
	g.now = func() time.Time { return now }
	for i := 0; i < MaxFailedAttempts; i++ {
		if g.locked("a") {
			t.Fatalf("locked after %d failures", i)
		}
		g.fail("a")
	}
	if !g.locked("a") || g.locked("b") {
		t.Fatal("the limit locks only that key")
	}
	// The lock ends when the window has passed.
	now = now.Add(AttemptWindow)
	if g.locked("a") {
		t.Fatal("still locked after the window")
	}
	// A correct password clears the count.
	for i := 0; i < MaxFailedAttempts-1; i++ {
		g.fail("c")
	}
	g.reset("c")
	g.fail("c")
	if g.locked("c") {
		t.Fatal("reset did not clear the count")
	}
}

func TestAttemptGuardMemoryIsBounded(t *testing.T) {
	g := newAttemptGuard()
	for i := 0; i < maxTrackedKeys+10; i++ {
		g.fail(string(rune(i)) + "x")
	}
	if len(g.fails) > maxTrackedKeys {
		t.Fatalf("tracked %d keys", len(g.fails))
	}
}
