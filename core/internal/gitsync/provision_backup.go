package gitsync

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/google/uuid"
)

type provisionBackupEntry struct {
	path string
	full string
	info fs.FileInfo
	uid  int
	gid  int
}

// backupProvisionRemovals creates a persistent, downloadable archive before
// the first destructive provisioning operation. Version 1 intentionally keeps
// this archive out of the generic file-only restore UI: automatic rollback is
// exact through same-filesystem staging, while the archive preserves complete
// directory metadata and content for explicit recovery.
func (s *Service) backupProvisionRemovals(ctx context.Context, binding StackBinding, commit, stackDirectory string, targetFS filesystem.FileSystem, targetRoot string, operations []normalizedProvisionOperation) (string, error) {
	entries, err := collectProvisionBackupEntries(ctx, targetFS, targetRoot, stackDirectory, operations)
	if err != nil || len(entries) == 0 {
		return "", err
	}
	if s.backupRoot == "" {
		return "", errors.New("Git stack backup directory is not configured")
	}
	if err := os.MkdirAll(s.backupRoot, 0700); err != nil {
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
	if err := backupFS.MkdirAll(binding.UUID, 0700); err != nil {
		return "", fmt.Errorf("create binding backup directory: %w", err)
	}
	backupID := uuid.NewString()
	createdAt := time.Now().UTC()
	relativePath := filepath.Join(binding.UUID, createdAt.Format("20060102T150405.000000000Z")+".tar.gz")
	handle, err := backupFS.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	manifest := backupManifest{Version: 1, BackupID: backupID, BindingID: binding.UUID,
		RepositoryID: binding.RepositoryUUID, Kind: "pre_provision_delete", CreatedAt: createdAt, CommitSHA: commit}
	gzipWriter := gzip.NewWriter(handle)
	tarWriter := tar.NewWriter(gzipWriter)
	writeErr := func() error {
		buffer := transferBufferPool.Get().([]byte)
		defer transferBufferPool.Put(buffer)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			header, err := tar.FileInfoHeader(entry.info, "")
			if err != nil {
				return err
			}
			header.Name, header.Uid, header.Gid, header.ModTime = entry.path, entry.uid, entry.gid, entry.info.ModTime()
			record := backupManifestFile{Path: entry.path, BeforeExists: true, BeforeMode: uint32(entry.info.Mode().Perm()),
				BeforeUID: entry.uid, BeforeGID: entry.gid, EntryType: "file"}
			if entry.info.IsDir() {
				header.Typeflag, header.Size, record.EntryType = tar.TypeDir, 0, "directory"
			}
			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}
			if entry.info.Mode().IsRegular() {
				reader, err := targetFS.OpenFile(entry.full, os.O_RDONLY, 0)
				if err != nil {
					return err
				}
				hash := sha256.New()
				written, copyErr := io.CopyBuffer(io.MultiWriter(tarWriter, hash), io.LimitReader(reader, entry.info.Size()), buffer)
				closeErr := reader.Close()
				if copyErr != nil || closeErr != nil || written != entry.info.Size() {
					return errors.Join(copyErr, closeErr, fmt.Errorf("archive %s: copied %d of %d bytes", entry.path, written, entry.info.Size()))
				}
				record.BeforeSHA = hex.EncodeToString(hash.Sum(nil))
			}
			manifest.Files = append(manifest.Files, record)
		}
		manifest.ComposePaths = uniqueComposePathsForFiles(binding, manifest.Files)
		manifestJSON, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: backupManifestName, Mode: 0600, Size: int64(len(manifestJSON)), ModTime: createdAt}); err != nil {
			return err
		}
		_, err = tarWriter.Write(manifestJSON)
		return err
	}()
	closeTarErr := tarWriter.Close()
	closeGzipErr := gzipWriter.Close()
	closeFileErr := handle.Close()
	if err := errors.Join(writeErr, closeTarErr, closeGzipErr, closeFileErr); err != nil {
		_ = backupFS.Remove(relativePath)
		return "", fmt.Errorf("write provisioning deletion backup: %w", err)
	}
	row, err := s.registerBackup(binding, backupID, "pre_provision_delete", relativePath, commit, manifest)
	if err != nil {
		_ = backupFS.Remove(relativePath)
		return "", fmt.Errorf("register provisioning deletion backup: %w", err)
	}
	s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
		Type: "backup_create", Trigger: "system", BackupID: row.UUID, CommitSHA: commit,
		Details: ActivityDetails{Action: "pre_provision_delete", Paths: manifest.ComposePaths}})
	return row.UUID, nil
}

func collectProvisionBackupEntries(ctx context.Context, targetFS filesystem.FileSystem, targetRoot, stackDirectory string, operations []normalizedProvisionOperation) ([]provisionBackupEntry, error) {
	bindingRoot, err := targetFS.Abs(targetRoot)
	if err != nil {
		return nil, err
	}
	stackRoot := targetFS.Join(targetRoot, filepath.FromSlash(stackDirectory))
	entries := make(map[string]provisionBackupEntry)
	var total int64
	for _, operation := range operations {
		if !operation.remove {
			continue
		}
		full := targetFS.Join(stackRoot, filepath.FromSlash(operation.path))
		if _, err := targetFS.Lstat(full); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		err := targetFS.WalkDir(full, func(current string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return walkErr
			}
			if entry == nil {
				return fmt.Errorf("cannot inspect removal target %s", current)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
				return fmt.Errorf("removal target contains unsupported or symbolic entry %s", current)
			}
			if isComposePath(current) || isProvisionControlPath(current) {
				return fmt.Errorf("removal target contains protected Compose or provisioning control file %s", current)
			}
			name := strings.ToLower(entry.Name())
			if info.IsDir() && (name == ".git" || name == ".dockman-backups" || strings.HasPrefix(name, ".dockman-provision-staging-")) {
				return fmt.Errorf("removal target contains Dockman or Git internal directory %s", current)
			}
			relative, err := filepath.Rel(bindingRoot, current)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if err := validateRelativePath(relative, false); err != nil {
				return err
			}
			if _, exists := entries[relative]; exists {
				return nil
			}
			if info.Mode().IsRegular() {
				total += info.Size()
			}
			if err := checkTransferLimit(len(entries)+1, info.Size(), total); err != nil {
				return fmt.Errorf("cannot create mandatory deletion backup at %s: %w", relative, err)
			}
			uid, gid, err := targetFS.Ownership(current)
			if err != nil {
				return err
			}
			entries[relative] = provisionBackupEntry{path: relative, full: current, info: info, uid: uid, gid: gid}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	result := make([]provisionBackupEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result, nil
}
