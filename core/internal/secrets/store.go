package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/host/filesystem"
)

const (
	RuntimeDirectory = ".secrets"
	MaxSecretBytes   = 1 << 20
)

var (
	ErrInvalidStackPath = errors.New("invalid stack path")
	ErrInvalidName      = errors.New("invalid secret name")
	ErrSecretTooLarge   = errors.New("secret exceeds the 1 MiB limit")
	secretNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// FileSystemProvider resolves an alias-qualified stack path for exactly one
// Docker host. Implementations must return a filesystem rooted at the alias
// and a path relative to that root.
type FileSystemProvider func(host, stackPath string) (filesystem.FileSystem, string, error)

// Store is deliberately small so encrypted source providers (SOPS/age) can be
// introduced without changing the HTTP or UI contract.
type Store interface {
	List(host, stackPath string) ([]Metadata, error)
	Read(host, stackPath, name string) ([]byte, error)
	Write(host, stackPath, name string, value []byte) (Metadata, error)
	Delete(host, stackPath, name string) error
}

type Metadata struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type PlainFileStore struct{ resolve FileSystemProvider }

func NewPlainFileStore(resolve FileSystemProvider) *PlainFileStore {
	return &PlainFileStore{resolve: resolve}
}

func (s *PlainFileStore) List(host, stackPath string) ([]Metadata, error) {
	stackFS, root, err := s.resolveStack(host, stackPath)
	if err != nil {
		return nil, err
	}
	directory := stackFS.Join(root, RuntimeDirectory)
	entries, err := stackFS.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return []Metadata{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list runtime secrets: %w", err)
	}
	result := make([]Metadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".dockman-secret-") || !validSecretName(entry.Name()) {
			continue
		}
		path := stackFS.Join(directory, entry.Name())
		info, statErr := stackFS.Lstat(path)
		if statErr != nil {
			return nil, fmt.Errorf("inspect runtime secret %q: %w", entry.Name(), statErr)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		result = append(result, metadata(entry.Name(), info))
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func (s *PlainFileStore) Read(host, stackPath, name string) ([]byte, error) {
	if !validSecretName(name) {
		return nil, ErrInvalidName
	}
	stackFS, root, err := s.resolveStack(host, stackPath)
	if err != nil {
		return nil, err
	}
	path := stackFS.Join(root, RuntimeDirectory, name)
	info, err := stackFS.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect runtime secret: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("runtime secret is not a regular file")
	}
	if info.Size() > MaxSecretBytes {
		return nil, ErrSecretTooLarge
	}
	value, err := stackFS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read runtime secret: %w", err)
	}
	if len(value) > MaxSecretBytes {
		clear(value)
		return nil, ErrSecretTooLarge
	}
	return value, nil
}

func (s *PlainFileStore) Write(host, stackPath, name string, value []byte) (Metadata, error) {
	if !validSecretName(name) {
		return Metadata{}, ErrInvalidName
	}
	if len(value) > MaxSecretBytes {
		return Metadata{}, ErrSecretTooLarge
	}
	stackFS, root, err := s.resolveStack(host, stackPath)
	if err != nil {
		return Metadata{}, err
	}
	directory := stackFS.Join(root, RuntimeDirectory)
	if err = stackFS.MkdirAll(directory, 0o700); err != nil {
		return Metadata{}, fmt.Errorf("create runtime secrets directory: %w", err)
	}
	if err = stackFS.Chmod(directory, 0o700); err != nil {
		return Metadata{}, fmt.Errorf("secure runtime secrets directory: %w", err)
	}

	temporary, err := temporaryName()
	if err != nil {
		return Metadata{}, err
	}
	temporaryPath := stackFS.Join(directory, temporary)
	destination := stackFS.Join(directory, name)
	file, err := stackFS.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Metadata{}, fmt.Errorf("create temporary runtime secret: %w", err)
	}
	written := false
	defer func() {
		if !written {
			_ = stackFS.RemoveAll(temporaryPath)
		}
	}()
	if _, err = file.Write(value); err != nil {
		_ = file.Close()
		return Metadata{}, fmt.Errorf("write temporary runtime secret: %w", err)
	}
	if err = file.Close(); err != nil {
		return Metadata{}, fmt.Errorf("close temporary runtime secret: %w", err)
	}
	if err = stackFS.Chmod(temporaryPath, 0o600); err != nil {
		return Metadata{}, fmt.Errorf("secure temporary runtime secret: %w", err)
	}
	if err = stackFS.Rename(temporaryPath, destination); err != nil {
		return Metadata{}, fmt.Errorf("atomically replace runtime secret: %w", err)
	}
	written = true
	if err = stackFS.Chmod(destination, 0o600); err != nil {
		return Metadata{}, fmt.Errorf("secure runtime secret: %w", err)
	}
	info, err := stackFS.Lstat(destination)
	if err != nil {
		return Metadata{}, fmt.Errorf("inspect written runtime secret: %w", err)
	}
	return metadata(name, info), nil
}

func (s *PlainFileStore) Delete(host, stackPath, name string) error {
	if !validSecretName(name) {
		return ErrInvalidName
	}
	stackFS, root, err := s.resolveStack(host, stackPath)
	if err != nil {
		return err
	}
	path := stackFS.Join(root, RuntimeDirectory, name)
	info, err := stackFS.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect runtime secret: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("runtime secret is not a regular file")
	}
	if err = stackFS.RemoveAll(path); err != nil {
		return fmt.Errorf("delete runtime secret: %w", err)
	}
	return nil
}

func (s *PlainFileStore) resolveStack(host, stackPath string) (filesystem.FileSystem, string, error) {
	host, stackPath = strings.TrimSpace(host), strings.TrimSpace(stackPath)
	if host == "" || stackPath == "" {
		return nil, "", ErrInvalidStackPath
	}
	stackFS, root, err := s.resolve(host, stackPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolve stack: %w", err)
	}
	root = filepath.Clean(root)
	if root == "" {
		return nil, "", ErrInvalidStackPath
	}
	info, err := stackFS.Stat(root)
	if err != nil {
		return nil, "", fmt.Errorf("inspect stack directory: %w", err)
	}
	if !info.IsDir() {
		return nil, "", fmt.Errorf("%w: path is not a directory", ErrInvalidStackPath)
	}
	return stackFS, root, nil
}

func validSecretName(name string) bool {
	return secretNamePattern.MatchString(name) && name != "." && name != ".."
}

func temporaryName() (string, error) {
	random := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", fmt.Errorf("generate temporary secret name: %w", err)
	}
	return ".dockman-secret-" + hex.EncodeToString(random), nil
}

func metadata(name string, info fs.FileInfo) Metadata {
	return Metadata{Name: name, Size: info.Size(), ModifiedAt: info.ModTime().UTC()}
}
