package gitsync

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
)

const vaultVersion byte = 1

type Vault struct{ aead cipher.AEAD }

func NewVault(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("Git credential encryption key must be exactly 32 bytes")
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
	gitDir := filepath.Join(configDir, "git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		return nil, "", err
	}
	if err := os.Chmod(gitDir, 0o700); err != nil {
		return nil, "", err
	}
	managedKey := false
	if strings.TrimSpace(keyFile) == "" {
		keyFile = filepath.Join(gitDir, "master.key")
		managedKey = true
	}
	raw, err := os.ReadFile(keyFile)
	if errors.Is(err, os.ErrNotExist) && managedKey {
		raw = make([]byte, 32)
		if _, err = io.ReadFull(rand.Reader, raw); err != nil {
			return nil, "", err
		}
		encoded := []byte(base64.StdEncoding.EncodeToString(raw) + "\n")
		if err = os.WriteFile(keyFile, encoded, 0o600); err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", fmt.Errorf("read Git master key: %w", err)
	} else {
		raw, err = decodeVaultKey(raw)
		if err != nil {
			return nil, "", err
		}
	}
	if managedKey {
		if err := os.Chmod(keyFile, 0o600); err != nil {
			return nil, "", err
		}
	}
	vault, err := NewVault(raw)
	return vault, keyFile, err
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
		return nil, fmt.Errorf("Git master key must contain 32 raw bytes or their base64 encoding")
	}
	return decoded, nil
}

func (v *Vault) Encrypt(plaintext []byte, credentialUUID string) ([]byte, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	aad := []byte("dockman/git-credential/" + credentialUUID)
	out := append([]byte{vaultVersion}, nonce...)
	return v.aead.Seal(out, nonce, plaintext, aad), nil
}

func (v *Vault) Decrypt(ciphertext []byte, credentialUUID string) ([]byte, error) {
	nonceSize := v.aead.NonceSize()
	if len(ciphertext) < 1+nonceSize || ciphertext[0] != vaultVersion {
		return nil, errors.New("unsupported or invalid encrypted credential")
	}
	nonce := ciphertext[1 : 1+nonceSize]
	aad := []byte("dockman/git-credential/" + credentialUUID)
	plain, err := v.aead.Open(nil, nonce, ciphertext[1+nonceSize:], aad)
	if err != nil {
		return nil, errors.New("unable to decrypt Git credential")
	}
	return plain, nil
}
