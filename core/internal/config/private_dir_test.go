package config

import (
	"os"
	"path/filepath"
	"testing"
)

// MkdirAll applies its mode only to directories it creates, so an installation
// that already exists keeps whatever an older release gave it: 0777 masked by
// the umask, usually 0755, on the directory holding the database and both
// master-key vaults. Upgrading has to repair that.
func TestAnExistingConfigDirectoryIsTightenedOnUpgrade(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}

	tightenPrivateDirectory(directory)

	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("config directory must be restricted to its owner, got %#o", got)
	}
}

// Owner access is exactly what must survive: the entrypoint chowns this
// directory to the user Dockman runs as, so 0700 keeps every access Dockman
// needs and removes only the ones nobody should have.
func TestTighteningKeepsEveryOwnerPermission(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "config")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	tightenPrivateDirectory(directory)

	probe := filepath.Join(directory, "dockman.db")
	if err := os.WriteFile(probe, []byte("state"), 0o600); err != nil {
		t.Fatalf("owner must still be able to write inside: %v", err)
	}
	if _, err := os.ReadFile(probe); err != nil {
		t.Fatalf("owner must still be able to read inside: %v", err)
	}
	if _, err := os.ReadDir(directory); err != nil {
		t.Fatalf("owner must still be able to list it: %v", err)
	}
}

// An already private directory is left exactly as it is, including a stricter
// mode an operator chose deliberately.
func TestAPrivateDirectoryIsLeftAlone(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o500} {
		directory := filepath.Join(t.TempDir(), "config")
		if err := os.MkdirAll(directory, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, mode); err != nil {
			t.Fatal(err)
		}
		tightenPrivateDirectory(directory)
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("mode %#o must be preserved, got %#o", mode, info.Mode().Perm())
		}
	}
}

// It must never fail startup, whatever it is pointed at.
func TestTighteningNeverPanicsOnAnUnusableTarget(t *testing.T) {
	tightenPrivateDirectory(filepath.Join(t.TempDir(), "missing"))
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tightenPrivateDirectory(file)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("a regular file must be left untouched, got %#o", info.Mode().Perm())
	}
}
