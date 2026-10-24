package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExplicitRecoveryLeavesBothFilesUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := run(path); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(`{"broken":`)
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(path); err == nil {
		t.Fatal("normal startup ignored corrupt state")
	}
	if err := runWithRecovery(path, true); err != nil {
		t.Fatal(err)
	}
	primary, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(primary) != string(corrupt) || string(after) != string(backup) {
		t.Fatal("recovery rewrote files")
	}
}
