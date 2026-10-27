package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupPrerequisite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.dump")
	if backupValid("") == nil || backupValid(path) == nil {
		t.Fatal("missing backup accepted")
	}
	if err := os.WriteFile(path, []byte("PGDMP"+string(make([]byte, 128))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := backupValid(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if backupValid(path) == nil {
		t.Fatal("public backup accepted")
	}
	_ = os.Chmod(path, 0600)
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(path, old, old)
	if backupValid(path) == nil {
		t.Fatal("stale backup accepted")
	}
	_ = os.WriteFile(path, []byte("not a database dump"+string(make([]byte, 128))), 0600)
	if backupValid(path) == nil {
		t.Fatal("invalid archive accepted")
	}
}
