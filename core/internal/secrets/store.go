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
	HistoryDirectory = ".history"
	MaxSecretBytes   = 1 << 20
	HistoryLimit     = 3
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
	ListHistory(host, stackPath, name string) ([]Version, error)
	Restore(host, stackPath, name, version string) (Metadata, error)
	AnalyzeCompose(host, stackPath string) (ComposeAnalysis, error)
	ListArchived(host, stackPath string) ([]ArchivedSecret, error)
	ListStacks(host string) ([]StackOption, error)
}

type ComposeSecret struct {
	Name        string   `json:"name"`
	File        string   `json:"file,omitempty"`
	RuntimeName string   `json:"runtimeName,omitempty"`
	Services    []string `json:"services"`
	External    bool     `json:"external"`
	Managed     bool     `json:"managed"`
	Exists      bool     `json:"exists"`
	Issue       string   `json:"issue,omitempty"`
}

type ComposeAnalysis struct {
	Manifests []string        `json:"manifests"`
	Secrets   []ComposeSecret `json:"secrets"`
}

type Metadata struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type Version struct {
	ID         string    `json:"id"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type ArchivedSecret struct {
	Name     string `json:"name"`
	Versions int    `json:"versions"`
}

type StackOption struct {
	Path      string   `json:"path"`
	Alias     string   `json:"alias"`
	Manifests []string `json:"manifests"`
}

type AliasProvider func(host string) ([]string, error)

type PlainFileStore struct {
	resolve FileSystemProvider
	aliases AliasProvider
}

func NewPlainFileStore(resolve FileSystemProvider) *PlainFileStore {
	return &PlainFileStore{resolve: resolve}
}

func (s *PlainFileStore) ConfigureAliases(provider AliasProvider) { s.aliases = provider }

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
	if err = s.backupExisting(stackFS, directory, name); err != nil {
		return Metadata{}, err
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
	if err = s.backupExisting(stackFS, stackFS.Join(root, RuntimeDirectory), name); err != nil {
		return err
	}
	if err = stackFS.RemoveAll(path); err != nil {
		return fmt.Errorf("delete runtime secret: %w", err)
	}
	return nil
}

func (s *PlainFileStore) ListHistory(host, stackPath, name string) ([]Version, error) {
	if !validSecretName(name) {
		return nil, ErrInvalidName
	}
	stackFS, root, err := s.resolveStack(host, stackPath)
	if err != nil {
		return nil, err
	}
	directory := stackFS.Join(root, RuntimeDirectory, HistoryDirectory, name)
	entries, err := stackFS.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return []Version{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list runtime secret history: %w", err)
	}
	versions := make([]Version, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !validVersionID(entry.Name()) {
			continue
		}
		info, statErr := stackFS.Lstat(stackFS.Join(directory, entry.Name()))
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		versions = append(versions, Version{ID: entry.Name(), Size: info.Size(), ModifiedAt: info.ModTime().UTC()})
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].ID > versions[j].ID })
	return versions, nil
}

func (s *PlainFileStore) ListArchived(host, stackPath string) ([]ArchivedSecret, error) {
	stackFS, root, err := s.resolveStack(host, stackPath)
	if err != nil {
		return nil, err
	}
	directory := stackFS.Join(root, RuntimeDirectory, HistoryDirectory)
	entries, err := stackFS.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return []ArchivedSecret{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list archived runtime secrets: %w", err)
	}
	current, err := s.List(host, stackPath)
	if err != nil {
		return nil, err
	}
	present := make(map[string]struct{}, len(current))
	for _, item := range current {
		present[item.Name] = struct{}{}
	}
	result := make([]ArchivedSecret, 0)
	for _, entry := range entries {
		if !entry.IsDir() || !validSecretName(entry.Name()) {
			continue
		}
		if _, ok := present[entry.Name()]; ok {
			continue
		}
		versions, listErr := s.ListHistory(host, stackPath, entry.Name())
		if listErr == nil && len(versions) > 0 {
			result = append(result, ArchivedSecret{Name: entry.Name(), Versions: len(versions)})
		}
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result, nil
}

func (s *PlainFileStore) Restore(host, stackPath, name, version string) (Metadata, error) {
	if !validSecretName(name) || !validVersionID(version) {
		return Metadata{}, ErrInvalidName
	}
	stackFS, root, err := s.resolveStack(host, stackPath)
	if err != nil {
		return Metadata{}, err
	}
	path := stackFS.Join(root, RuntimeDirectory, HistoryDirectory, name, version)
	info, err := stackFS.Lstat(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("inspect runtime secret version: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSecretBytes {
		return Metadata{}, errors.New("runtime secret version is not a valid regular file")
	}
	value, err := stackFS.ReadFile(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("read runtime secret version: %w", err)
	}
	defer clear(value)
	return s.Write(host, stackPath, name, value)
}

func (s *PlainFileStore) backupExisting(stackFS filesystem.FileSystem, directory, name string) error {
	source := stackFS.Join(directory, name)
	info, err := stackFS.Lstat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runtime secret before backup: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > MaxSecretBytes {
		return errors.New("runtime secret cannot be backed up safely")
	}
	value, err := stackFS.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read runtime secret before backup: %w", err)
	}
	defer clear(value)
	history := stackFS.Join(directory, HistoryDirectory, name)
	if err = stackFS.MkdirAll(history, 0o700); err != nil {
		return fmt.Errorf("create runtime secret history: %w", err)
	}
	if err = stackFS.Chmod(stackFS.Join(directory, HistoryDirectory), 0o700); err != nil {
		return fmt.Errorf("secure runtime secret history root: %w", err)
	}
	if err = stackFS.Chmod(history, 0o700); err != nil {
		return fmt.Errorf("secure runtime secret history: %w", err)
	}
	random, err := temporaryName()
	if err != nil {
		return err
	}
	version := time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + strings.TrimPrefix(random, ".dockman-secret-")
	path := stackFS.Join(history, version)
	file, err := stackFS.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create runtime secret backup: %w", err)
	}
	if _, err = file.Write(value); err != nil {
		_ = file.Close()
		_ = stackFS.RemoveAll(path)
		return fmt.Errorf("write runtime secret backup: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = stackFS.RemoveAll(path)
		return fmt.Errorf("close runtime secret backup: %w", err)
	}
	if err = stackFS.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure runtime secret backup: %w", err)
	}
	return pruneHistory(stackFS, history)
}

func pruneHistory(stackFS filesystem.FileSystem, directory string) error {
	entries, err := stackFS.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("list runtime secret history for retention: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && validVersionID(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	if len(ids) <= HistoryLimit {
		return nil
	}
	for _, id := range ids[HistoryLimit:] {
		if err = stackFS.RemoveAll(stackFS.Join(directory, id)); err != nil {
			return fmt.Errorf("prune runtime secret history: %w", err)
		}
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

func validVersionID(value string) bool {
	return len(value) > 20 && len(value) < 80 && !strings.ContainsAny(value, `/\\`) && value != "." && value != ".."
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
