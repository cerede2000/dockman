package notifications

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

type Vault struct{ aead cipher.AEAD }

func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("notification encryption key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

func LoadOrCreateVault(configDir, keyFile string) (*Vault, string, error) {
	dir := filepath.Join(configDir, "notifications")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, "", err
	}
	managed := strings.TrimSpace(keyFile) == ""
	if managed {
		keyFile = filepath.Join(dir, "master.key")
	}
	raw, err := os.ReadFile(keyFile)
	if errors.Is(err, os.ErrNotExist) && managed {
		raw = make([]byte, 32)
		if _, err = io.ReadFull(rand.Reader, raw); err != nil {
			return nil, "", err
		}
		if err = os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(raw)+"\n"), 0o600); err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", fmt.Errorf("read notification master key: %w", err)
	} else {
		raw, err = decodeVaultKey(raw)
		if err != nil {
			return nil, "", err
		}
	}
	if managed {
		if err := os.Chmod(keyFile, 0o600); err != nil {
			return nil, "", err
		}
	}
	warnIfKeyFileIsExposed(keyFile)
	vault, err := NewVault(raw)
	return vault, keyFile, err
}

// warnIfKeyFileIsExposed reports a master key readable beyond its owner. The
// secrets subsystem refuses such a key outright; this one warns instead, and
// the difference is deliberate: that key file is a path the operator chose and
// may deliberately share with another process, and refusing here would stop
// Dockman from starting over a file mode. It is never silent though - anyone
// who can read this file can decrypt every stored notification credential.
func warnIfKeyFileIsExposed(keyFile string) {
	info, err := os.Stat(keyFile)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		log.Warn().Str("key_file", keyFile).Str("mode", fmt.Sprintf("%#o", mode)).
			Msgf("Master key is readable beyond its owner; anyone with that access can decrypt every stored notification credential. Restrict it with: chmod 600 %s", keyFile)
	}
}

func decodeVaultKey(raw []byte) ([]byte, error) {
	if len(raw) == 32 {
		return raw, nil
	}
	trimmed := []byte(strings.TrimSpace(string(raw)))
	if len(trimmed) == 32 {
		return trimmed, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(string(trimmed))
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("notification master key must contain 32 raw bytes or their base64 encoding")
	}
	return decoded, nil
}

func (v *Vault) Encrypt(plaintext []byte, host string) ([]byte, error) {
	return v.EncryptFor(plaintext, "smtp/"+host)
}

func (v *Vault) EncryptFor(plaintext []byte, scope string) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := append([]byte{vaultVersion}, nonce...)
	return v.aead.Seal(out, nonce, plaintext, []byte("dockman/"+scope)), nil
}

func (v *Vault) Decrypt(ciphertext []byte, host string) ([]byte, error) {
	return v.DecryptFor(ciphertext, "smtp/"+host)
}

func (v *Vault) DecryptFor(ciphertext []byte, scope string) ([]byte, error) {
	nonceSize := v.aead.NonceSize()
	if len(ciphertext) < 1+nonceSize || ciphertext[0] != vaultVersion {
		return nil, errors.New("unsupported or invalid encrypted SMTP credential")
	}
	nonce := ciphertext[1 : 1+nonceSize]
	plain, err := v.aead.Open(nil, nonce, ciphertext[1+nonceSize:], []byte("dockman/"+scope))
	if err != nil {
		return nil, errors.New("unable to decrypt notification credential")
	}
	return plain, nil
}
