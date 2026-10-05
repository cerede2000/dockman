// Package credvault encrypts the credentials Dockman stores for itself:
// registry logins today, and whatever subsystem adopts it next.
//
// Git and notifications each carry their own copy of this code, with their own
// master key file. The copies are left alone - rewriting the storage of
// credentials already in use is its own subject - but a third copy is not
// written: this package is that code, with the subsystem's name as a
// parameter, so a new one inherits the same file modes, the same key handling
// and the same domain separation instead of a near-copy that drifts.
package credvault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"
)

const vaultVersion byte = 1

// Vault seals values with AES-256-GCM, binding each to a scope so a ciphertext
// cannot be moved from one credential - or one subsystem - to another.
type Vault struct {
	aead cipher.AEAD
	name string
}

// New builds a vault from a 32-byte key. name appears in error messages and
// names nothing else: the scope passed to EncryptFor is what separates
// subsystems cryptographically.
func New(name string, key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%s encryption key must be exactly 32 bytes", name)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead, name: name}, nil
}

// LoadOrCreate opens the vault of a subsystem. keyFile is the operator's own
// path when set; otherwise the key lives in <configDir>/<dir>/master.key and
// is generated on first use. It returns the key file in use, so the caller can
// tell the operator which file to back up.
func LoadOrCreate(name, configDir, dir, keyFile string) (*Vault, string, error) {
	home := filepath.Join(configDir, dir)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, "", err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return nil, "", err
	}
	managedKey := false
	if strings.TrimSpace(keyFile) == "" {
		keyFile = filepath.Join(home, "master.key")
		managedKey = true
	}
	raw, err := os.ReadFile(keyFile)
	switch {
	case errors.Is(err, os.ErrNotExist) && managedKey:
		raw = make([]byte, 32)
		if _, err = io.ReadFull(rand.Reader, raw); err != nil {
			return nil, "", err
		}
		encoded := []byte(base64.StdEncoding.EncodeToString(raw) + "\n")
		if err = os.WriteFile(keyFile, encoded, 0o600); err != nil {
			return nil, "", err
		}
	case err != nil:
		return nil, "", fmt.Errorf("read %s master key: %w", name, err)
	default:
		raw, err = decodeKey(name, raw)
		if err != nil {
			return nil, "", err
		}
	}
	if managedKey {
		if err := os.Chmod(keyFile, 0o600); err != nil {
			return nil, "", err
		}
	}
	warnIfKeyFileIsExposed(keyFile)
	vault, err := New(name, raw)
	return vault, keyFile, err
}

// warnIfKeyFileIsExposed reports a master key readable beyond its owner. It
// warns rather than refuses, as the Git vault does: the path is one the
// operator chose and may deliberately share with another process. It is never
// silent though - anyone who can read this file can decrypt every stored
// credential.
func warnIfKeyFileIsExposed(keyFile string) {
	info, err := os.Stat(keyFile)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		log.Warn().Str("key_file", keyFile).Str("mode", fmt.Sprintf("%#o", mode)).
			Msgf("Master key is readable beyond its owner; anyone with that access can decrypt every stored credential. Restrict it with: chmod 600 %s", keyFile)
	}
}

func decodeKey(name string, raw []byte) ([]byte, error) {
	if len(raw) == 32 {
		return raw, nil
	}
	trimmed := []byte(strings.TrimSpace(string(raw)))
	if len(trimmed) == 32 {
		return trimmed, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(string(trimmed))
	if err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("%s master key must contain 32 raw bytes or their base64 encoding", name)
	}
	return decoded, nil
}

func (v *Vault) EncryptFor(plaintext []byte, scope string) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	aad := []byte("dockman/" + scope)
	out := append([]byte{vaultVersion}, nonce...)
	return v.aead.Seal(out, nonce, plaintext, aad), nil
}

func (v *Vault) DecryptFor(ciphertext []byte, scope string) ([]byte, error) {
	nonceSize := v.aead.NonceSize()
	if len(ciphertext) < 1+nonceSize || ciphertext[0] != vaultVersion {
		return nil, errors.New("unsupported or invalid encrypted credential")
	}
	nonce := ciphertext[1 : 1+nonceSize]
	aad := []byte("dockman/" + scope)
	plain, err := v.aead.Open(nil, nonce, ciphertext[1+nonceSize:], aad)
	if err != nil {
		return nil, fmt.Errorf("unable to decrypt %s credential", v.name)
	}
	return plain, nil
}
