package gitsync

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/rs/zerolog/log"
)

const (
	compactStorageMarker   = "dockman-object-store-v1"
	compactMigrationMarker = "dockman-object-store-converting"
	repositoryStorageMode  = "compact"
)

// ensureCompactRepository converts repositories created by older Dockman
// releases to a worktree-less object store. The marker lives in .git so it
// never changes the repository status. Conversion is deliberately refused
// when the legacy worktree contains modified, untracked, or ignored data.
func ensureCompactRepository(path string) error {
	gitDirectory := filepath.Join(path, ".git")
	finalMarker := filepath.Join(gitDirectory, compactStorageMarker)
	convertingMarker := filepath.Join(gitDirectory, compactMigrationMarker)
	if markerExists(finalMarker) {
		return nil
	}
	if !markerExists(convertingMarker) {
		repo, err := gitclient.PlainOpen(path)
		if err != nil {
			return fmt.Errorf("open legacy repository workspace: %w", err)
		}
		worktree, err := repo.Worktree()
		if err != nil {
			return fmt.Errorf("inspect legacy repository workspace: %w", err)
		}
		status, err := worktree.Status()
		if err != nil {
			return fmt.Errorf("inspect legacy repository changes: %w", err)
		}
		if !status.IsClean() {
			return errors.New("compact Git storage migration refused: the legacy repository workspace contains uncommitted changes")
		}
		if err := verifyLegacyWorktreeContainsOnlyTracked(repo, path); err != nil {
			return err
		}
		if err := writeStorageMarker(convertingMarker); err != nil {
			return fmt.Errorf("start compact Git storage migration: %w", err)
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read legacy repository workspace: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return fmt.Errorf("remove legacy repository checkout %q: %w", entry.Name(), err)
		}
	}
	if err := writeStorageMarker(finalMarker); err != nil {
		return fmt.Errorf("complete compact Git storage migration: %w", err)
	}
	if err := os.Remove(convertingMarker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("finish compact Git storage migration: %w", err)
	}
	return nil
}

func verifyLegacyWorktreeContainsOnlyTracked(repo *gitclient.Repository, root string) error {
	index, err := repo.Storer.Index()
	if err != nil {
		return fmt.Errorf("inspect legacy repository index: %w", err)
	}
	tracked := make(map[string]struct{}, len(index.Entries))
	directories := map[string]struct{}{".": {}}
	for _, entry := range index.Entries {
		normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Name)))
		if normalized != entry.Name || validateRelativePath(normalized, false) != nil {
			return fmt.Errorf("compact Git storage migration refused: unsafe index path %q", entry.Name)
		}
		relative := filepath.FromSlash(normalized)
		if entry.Mode == filemode.Submodule {
			return fmt.Errorf("compact Git storage migration refused: submodule %q requires manual removal or conversion", entry.Name)
		}
		tracked[relative] = struct{}{}
		for directory := filepath.Dir(relative); directory != "."; directory = filepath.Dir(directory) {
			directories[directory] = struct{}{}
		}
	}
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || relative == "." {
			return err
		}
		if relative == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			if _, ok := directories[relative]; !ok {
				return fmt.Errorf("compact Git storage migration refused: the legacy workspace contains untracked or ignored data at %q", filepath.ToSlash(relative))
			}
			return nil
		}
		if _, ok := tracked[relative]; !ok {
			return fmt.Errorf("compact Git storage migration refused: the legacy workspace contains untracked or ignored data at %q", filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func markerExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func writeStorageMarker(path string) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if _, err := temporary.WriteString("1\n"); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func markCompactRepository(path string) error {
	return writeStorageMarker(filepath.Join(path, ".git", compactStorageMarker))
}

func temporaryRepositoryWorktree(repo *gitclient.Repository, workspaceRoot string) (*gitclient.Repository, string, func(), error) {
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		return nil, "", nil, err
	}
	cleanupStaleTemporaryWorktrees(workspaceRoot, time.Now())
	directory, err := os.MkdirTemp(workspaceRoot, ".dockman-export-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("create temporary Git checkout: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	temporary, err := gitclient.Open(repo.Storer, osfs.New(directory))
	if err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("open temporary Git checkout: %w", err)
	}
	return temporary, directory, cleanup, nil
}

func cleanupStaleTemporaryWorktrees(workspaceRoot string, now time.Time) {
	const staleAfter = 24 * time.Hour
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".dockman-export-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < staleAfter {
			continue
		}
		path := filepath.Join(workspaceRoot, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			log.Warn().Err(err).Str("path", entry.Name()).Msg("Unable to remove stale temporary Git checkout")
		}
	}
}

func repositoryCommitTree(repo *gitclient.Repository, branch string) (*object.Tree, error) {
	reference, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return nil, fmt.Errorf("resolve repository branch %q: %w", branch, err)
	}
	commit, err := repo.CommitObject(reference.Hash())
	if err != nil {
		return nil, fmt.Errorf("read repository commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("read repository tree: %w", err)
	}
	return tree, nil
}

func repositoryCommitTreeAtHash(repo *gitclient.Repository, hash plumbing.Hash) (*object.Tree, error) {
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("read repository commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("read repository tree: %w", err)
	}
	return tree, nil
}

func repositorySubtree(tree *object.Tree, subPath string) (*object.Tree, error) {
	if subPath == "." || strings.TrimSpace(subPath) == "" {
		return tree, nil
	}
	subtree, err := tree.Tree(filepath.ToSlash(subPath))
	if errors.Is(err, object.ErrDirectoryNotFound) {
		return nil, os.ErrNotExist
	}
	return subtree, err
}

func gitFileMode(mode filemode.FileMode) fs.FileMode {
	if mode == filemode.Executable {
		return 0o755
	}
	return 0o644
}

func gitBlobOpener(repo *gitclient.Repository, hash plumbing.Hash) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		blob, err := repo.BlobObject(hash)
		if err != nil {
			return nil, err
		}
		return blob.Reader()
	}
}
