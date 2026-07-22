package gitsync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadUintFileAndPositiveDifference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.current")
	if err := os.WriteFile(path, []byte("157286400\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readUintFile(path); got != 157286400 {
		t.Fatalf("readUintFile() = %d", got)
	}
	if got := positiveDifference(20, 10); got != 10 {
		t.Fatalf("positiveDifference() = %d", got)
	}
	if got := positiveDifference(10, 20); got != 0 {
		t.Fatalf("positiveDifference underflow = %d", got)
	}
}
