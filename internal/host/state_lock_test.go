package host

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAStateFileHasOneHostOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	unlock, err := LockState(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := LockState(path); err == nil {
		t.Fatal("a second host took the same state file")
	} else if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second lock: %v", err)
	}

	unlock()
	again, err := LockState(path)
	if err != nil {
		t.Fatalf("the released state file stayed locked: %v", err)
	}
	again()
}
