package secrets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// GuardFileDeletion releases an encrypted stack's volatile runtime before the
// file service removes its directory.
//
// Without it, deleting the folder of a stack whose tmpfs is mounted goes
// half-way and stops: RemoveAll walks the tree, deletes secrets.sops.yaml and
// everything around it, then fails on the mount point itself with EBUSY. The
// user is left with a stack directory that is neither there nor gone and a
// dangling mount that only a reboot or a manual umount clears - and the
// ciphertext, the one thing that was worth keeping, is already gone.
//
// Deleting a stack is an explicit, confirmed act, so releasing the mount as
// part of it is the right behaviour rather than refusing. If the release does
// not complete the deletion is refused outright, which leaves the stack whole.
func (s *Service) GuardFileDeletion(host, filename string) error {
	if s.encrypted == nil {
		return nil
	}
	stackPath, err := s.encryptedStackUnder(host, filename)
	if err != nil || stackPath == "" {
		return err
	}
	stackFS, root, err := s.encrypted.resolveStack(host, stackPath)
	if err != nil {
		// The stack cannot be resolved, so nothing here can be mounted either.
		return nil
	}
	volatile, err := s.encrypted.volatileRuntimeAvailable(context.Background(), host, stackFS, root)
	if err != nil || !volatile {
		return err
	}
	if err := s.encrypted.releaseVolatileRuntime(context.Background(), host, stackFS, root); err != nil {
		return fmt.Errorf("refusing to delete %s: its encrypted secrets are still mounted in memory and could not be released, which would leave the directory half-removed and the mount behind: %w", stackPath, err)
	}
	return nil
}

// encryptedStackUnder reports the encrypted stack the deletion targets, whether
// the path is the stack directory itself or an ancestor of it. It returns an
// empty path when nothing encrypted is involved.
func (s *Service) encryptedStackUnder(host, filename string) (string, error) {
	filename = strings.Trim(strings.TrimSpace(filename), "/")
	if filename == "" {
		return "", nil
	}
	// The common case: the path is the stack directory.
	if enabled, err := s.encrypted.InlineEnabled(host, filename); err == nil && enabled {
		return filename, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	// Deleting an ancestor takes its encrypted stacks with it. Only one level
	// of children is examined: a deeper tree is caught by the mount check on
	// the way, and an unbounded walk here would cost more than it saves.
	stacks, err := s.ListStacks(host)
	if err != nil {
		return "", nil
	}
	prefix := filename + "/"
	for _, stack := range stacks {
		if stack.Path != filename && !strings.HasPrefix(stack.Path, prefix) {
			continue
		}
		if enabled, enabledErr := s.encrypted.InlineEnabled(host, stack.Path); enabledErr == nil && enabled {
			return stack.Path, nil
		}
	}
	return "", nil
}
