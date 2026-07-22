package gitsync

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVaultRoundTripAndBinding(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	vault, err := NewVault(key)
	require.NoError(t, err)

	first, err := vault.Encrypt([]byte("super-secret-token"), "credential-a")
	require.NoError(t, err)
	second, err := vault.Encrypt([]byte("super-secret-token"), "credential-a")
	require.NoError(t, err)
	require.NotEqual(t, first, second, "a fresh nonce must be used for every encryption")
	require.NotContains(t, string(first), "super-secret-token")

	plain, err := vault.Decrypt(first, "credential-a")
	require.NoError(t, err)
	require.Equal(t, []byte("super-secret-token"), plain)
	_, err = vault.Decrypt(first, "credential-b")
	require.Error(t, err, "ciphertext must be bound to its credential UUID")
}

func TestLoadOrCreateVaultPermissionsAndStableKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	dir := t.TempDir()
	first, path, err := LoadOrCreateVault(dir, "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "git", "master.key"), path)

	keyInfo, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())
	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	ciphertext, err := first.Encrypt([]byte("persistent"), "id")
	require.NoError(t, err)
	second, _, err := LoadOrCreateVault(dir, "")
	require.NoError(t, err)
	plain, err := second.Decrypt(ciphertext, "id")
	require.NoError(t, err)
	require.Equal(t, "persistent", string(plain))
}

func TestDecodeVaultKey(t *testing.T) {
	key := bytes.Repeat([]byte("k"), 32)
	decoded, err := decodeVaultKey([]byte(base64.StdEncoding.EncodeToString(key) + "\n"))
	require.NoError(t, err)
	require.Equal(t, key, decoded)
	_, err = decodeVaultKey([]byte("too-short"))
	require.Error(t, err)
}
