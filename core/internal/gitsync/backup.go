package gitsync

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const backupManifestName = ".dockman-backup-manifest.json"

type backupManifestFile struct {
	Path         string `json:"path"`
	BeforeSHA    string `json:"beforeSha,omitempty"`
	AfterSHA     string `json:"afterSha,omitempty"`
	BeforeExists bool   `json:"beforeExists"`
	AfterExists  bool   `json:"afterExists"`
	BeforeMode   uint32 `json:"beforeMode,omitempty"`
}

type backupManifest struct {
	Version      int                  `json:"version"`
	BackupID     string               `json:"backupId"`
	BindingID    string               `json:"bindingId"`
	RepositoryID string               `json:"repositoryId"`
	Kind         string               `json:"kind"`
	CreatedAt    time.Time            `json:"createdAt"`
	CommitSHA    string               `json:"commitSha,omitempty"`
	ComposePaths []string             `json:"composePaths,omitempty"`
	Files        []backupManifestFile `json:"files"`
}

type BackupView struct {
	ID           string     `json:"id"`
	BindingID    string     `json:"bindingId"`
	RepositoryID string     `json:"repositoryId"`
	Kind         string     `json:"kind"`
	ComposePaths []string   `json:"composePaths"`
	CommitSHA    string     `json:"commitSha,omitempty"`
	FileCount    int        `json:"fileCount"`
	SizeBytes    int64      `json:"sizeBytes"`
	Restorable   bool       `json:"restorable"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
}

func (s *Service) backupChangedFiles(binding StackBinding, source, target map[string]transferFile, kind, commit string, protectedBackupIDs ...string) (string, error) {
	if s.backupRoot == "" {
		return "", errors.New("Git stack backup directory is not configured")
	}
	if err := os.MkdirAll(s.backupRoot, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	rootInfo, err := os.Lstat(s.backupRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Git stack backup root must be a real directory")
	}
	backupFS, err := os.OpenRoot(s.backupRoot)
	if err != nil {
		return "", fmt.Errorf("open backup directory: %w", err)
	}
	defer backupFS.Close()
	if err := backupFS.MkdirAll(binding.UUID, 0o700); err != nil {
		return "", fmt.Errorf("create binding backup directory: %w", err)
	}
	backupID := uuid.NewString()
	createdAt := time.Now().UTC()
	name := createdAt.Format("20060102T150405.000000000Z") + ".tar.gz"
	relativePath := filepath.Join(binding.UUID, name)
	handle, err := backupFS.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	manifest := backupManifest{Version: 2, BackupID: backupID, BindingID: binding.UUID,
		RepositoryID: binding.RepositoryUUID, Kind: kind, CreatedAt: createdAt, CommitSHA: commit}
	for _, sourceFile := range sortedTransferFiles(source) {
		targetFile, exists := target[sourceFile.path]
		record := backupManifestFile{Path: sourceFile.path, AfterSHA: sourceFile.sha, AfterExists: sourceFile.open != nil}
		if !exists {
			manifest.Files = append(manifest.Files, record)
			continue
		}
		if targetFile.open == nil || sourceFile.sha == targetFile.sha {
			continue
		}
		record.BeforeExists, record.BeforeSHA, record.BeforeMode = true, targetFile.sha, uint32(safeFileMode(targetFile.mode))
		manifest.Files = append(manifest.Files, record)
	}
	manifest.ComposePaths = uniqueComposePathsForFiles(binding, manifest.Files)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		_ = handle.Close()
		_ = backupFS.Remove(relativePath)
		return "", err
	}
	gzipWriter := gzip.NewWriter(handle)
	tarWriter := tar.NewWriter(gzipWriter)
	writeErr := func() error {
		header := &tar.Header{Name: backupManifestName, Mode: 0o600, Size: int64(len(manifestJSON)), ModTime: createdAt}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tarWriter.Write(manifestJSON); err != nil {
			return err
		}
		for _, record := range manifest.Files {
			if !record.BeforeExists {
				continue
			}
			targetFile := target[record.Path]
			header := &tar.Header{Name: record.Path, Mode: int64(safeFileMode(targetFile.mode)), Size: targetFile.size, ModTime: createdAt}
			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}
			if err := streamTransferFile(targetFile, tarWriter); err != nil {
				return err
			}
		}
		return nil
	}()
	closeTarErr := tarWriter.Close()
	closeGzipErr := gzipWriter.Close()
	closeFileErr := handle.Close()
	for _, candidate := range []error{writeErr, closeTarErr, closeGzipErr, closeFileErr} {
		if candidate != nil {
			_ = backupFS.Remove(relativePath)
			return "", fmt.Errorf("write stack backup: %w", candidate)
		}
	}
	row, err := s.registerBackup(binding, backupID, kind, relativePath, commit, manifest, protectedBackupIDs...)
	if err != nil {
		_ = backupFS.Remove(relativePath)
		return "", fmt.Errorf("register stack backup: %w", err)
	}
	s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
		Type: "backup_create", Trigger: "system", BackupID: row.UUID, CommitSHA: commit,
		Details: ActivityDetails{Action: kind, Paths: manifest.ComposePaths}})
	return row.UUID, nil
}

func backupView(row GitBackup) BackupView {
	return BackupView{ID: row.UUID, BindingID: row.BindingUUID, RepositoryID: row.RepositoryUUID,
		Kind: row.Kind, ComposePaths: splitPatternLines(row.ComposePaths), CommitSHA: row.CommitSHA,
		FileCount: row.FileCount, SizeBytes: row.SizeBytes, Restorable: row.Restorable,
		ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt}
}

func (s *Service) ListBindingBackups(bindingID string, limit int) ([]BackupView, error) {
	if _, err := s.store.GetBinding(bindingID); err != nil {
		return nil, err
	}
	rows, err := s.store.ListBindingBackups(bindingID, limit)
	if err != nil {
		return nil, err
	}
	views := make([]BackupView, 0, len(rows))
	for _, row := range rows {
		views = append(views, backupView(row))
	}
	return views, nil
}

func (s *Service) OpenBackup(bindingID, backupID string) (io.ReadCloser, string, error) {
	row, err := s.authorizedBackup(bindingID, backupID)
	if err != nil {
		return nil, "", err
	}
	root, err := s.openBackupRoot()
	if err != nil {
		return nil, "", err
	}
	handle, err := root.Open(filepath.FromSlash(row.ArchivePath))
	if err != nil {
		root.Close()
		return nil, "", err
	}
	return &rootBoundReadCloser{ReadCloser: handle, root: root}, filepath.Base(row.ArchivePath), nil
}

type rootBoundReadCloser struct {
	io.ReadCloser
	root *os.Root
}

func (r *rootBoundReadCloser) Close() error {
	return errors.Join(r.ReadCloser.Close(), r.root.Close())
}

func (s *Service) DeleteBackup(bindingID, backupID string) error {
	row, err := s.authorizedBackup(bindingID, backupID)
	if err != nil {
		return err
	}
	if err := s.removeBackupArchive(row); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.store.DeleteBackup(row.UUID); err != nil {
		return err
	}
	s.recordActivity(ActivityRecord{RepositoryID: row.RepositoryUUID, BindingID: row.BindingUUID,
		Type: "backup_delete", Trigger: "manual", Details: ActivityDetails{Action: "delete", Paths: splitPatternLines(row.ComposePaths)}})
	return nil
}

func (s *Service) authorizedBackup(bindingID, backupID string) (GitBackup, error) {
	if _, err := uuid.Parse(backupID); err != nil {
		return GitBackup{}, errors.New("invalid backup identifier")
	}
	row, err := s.store.GetBackup(backupID)
	if err != nil {
		return GitBackup{}, err
	}
	if row.BindingUUID != bindingID {
		return GitBackup{}, gorm.ErrRecordNotFound
	}
	return row, nil
}

func (s *Service) openBackupRoot() (*os.Root, error) {
	info, err := os.Lstat(s.backupRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Git stack backup root must be a real directory")
	}
	return os.OpenRoot(s.backupRoot)
}

func (s *Service) removeBackupArchive(row GitBackup) error {
	root, err := s.openBackupRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(filepath.FromSlash(row.ArchivePath))
}

func (s *Service) registerBackup(binding StackBinding, id, kind, archivePath, commit string, manifest backupManifest, protectedBackupIDs ...string) (GitBackup, error) {
	info, err := os.Stat(filepath.Join(s.backupRoot, filepath.FromSlash(archivePath)))
	if err != nil {
		return GitBackup{}, err
	}
	expires := time.Now().UTC().AddDate(0, 0, s.backupRetentionDays)
	row := GitBackup{UUID: id, RepositoryUUID: binding.RepositoryUUID, BindingUUID: binding.UUID,
		Kind: kind, ComposePaths: strings.Join(manifest.ComposePaths, "\n"), ArchivePath: filepath.ToSlash(archivePath),
		CommitSHA: commit, FileCount: len(manifest.Files), SizeBytes: info.Size(), Restorable: manifest.Version >= 2,
		ExpiresAt: &expires}
	if err := s.store.SaveBackup(&row); err != nil {
		return GitBackup{}, err
	}
	if err := s.pruneManagedBackups(binding.UUID, time.Now().UTC(), protectedBackupIDs...); err != nil {
		_ = s.store.DeleteBackup(row.UUID)
		return GitBackup{}, err
	}
	return row, nil
}

func (s *Service) pruneManagedBackups(bindingID string, now time.Time, protectedBackupIDs ...string) error {
	rows, err := s.store.ListBindingBackups(bindingID, 200)
	if err != nil {
		return err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	protected := make(map[string]struct{}, len(protectedBackupIDs))
	for _, id := range protectedBackupIDs {
		protected[id] = struct{}{}
	}
	retained := 0
	for _, row := range rows {
		if _, keep := protected[row.UUID]; keep {
			retained++
			continue
		}
		expired := row.ExpiresAt != nil && row.ExpiresAt.Before(now)
		if retained < gitBackupRetention && !expired {
			retained++
			continue
		}
		if err := s.removeBackupArchive(row); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := s.store.DeleteBackup(row.UUID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) pruneExpiredBackups(now time.Time) error {
	rows, err := s.store.ExpiredBackups(now)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.removeBackupArchive(row); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := s.store.DeleteBackup(row.UUID); err != nil {
			return err
		}
	}
	return nil
}

func readBackupManifest(handle io.Reader) (backupManifest, error) {
	gzipReader, err := gzip.NewReader(handle)
	if err != nil {
		return backupManifest{}, err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return backupManifest{}, errors.New("backup manifest is missing")
		}
		if err != nil {
			return backupManifest{}, err
		}
		if header.Name != backupManifestName {
			continue
		}
		if header.Size < 1 || header.Size > 2<<20 {
			return backupManifest{}, errors.New("invalid backup manifest size")
		}
		var manifest backupManifest
		if err := json.NewDecoder(io.LimitReader(tarReader, header.Size)).Decode(&manifest); err != nil {
			return backupManifest{}, err
		}
		return manifest, nil
	}
}

func uniqueComposePathsForFiles(binding StackBinding, files []backupManifestFile) []string {
	allCompose := splitPatternLines(binding.ComposePaths)
	paths := make([]string, 0)
	for _, file := range files {
		paths = append(paths, composePathsForFile(allCompose, file.Path)...)
	}
	return uniqueSortedStrings(paths)
}

func validateBackupManifest(manifest backupManifest, row GitBackup) error {
	if manifest.Version < 2 || manifest.BackupID != row.UUID || manifest.BindingID != row.BindingUUID {
		return errors.New("backup is not eligible for safe automatic restoration")
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > maxBindingFiles {
		return errors.New("invalid backup file inventory")
	}
	for _, file := range manifest.Files {
		if err := validateRelativePath(file.Path, false); err != nil {
			return fmt.Errorf("invalid backup path: %w", err)
		}
	}
	return nil
}
