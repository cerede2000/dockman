package secrets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
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
	// Deleting the runtime directory itself is not a deletion at all: it is a
	// live mount point the host daemon owns, and it puts it straight back as
	// long as the stack is marked encrypted. RemoveAll would first wipe the
	// materialized plaintext of a running stack, then fail on the mount with
	// EBUSY - destructive and pointless in the same move. Say why instead.
	if stack, isRuntime := runtimeDirectoryOwner(filename); isRuntime {
		if enabled, enabledErr := s.encrypted.InlineEnabled(host, stack); enabledErr == nil && enabled {
			return fmt.Errorf("%s is the mounted secret runtime of an encrypted stack, not an ordinary folder: the host daemon owns it and recreates it. Leave encrypted mode for %s first, which unmounts it", filename, stack)
		}
	}
	stackPaths, err := s.encryptedStacksUnder(host, filename)
	if err != nil || len(stackPaths) == 0 {
		return err
	}
	// Every one of them, not just the first. Releasing one and leaving the next
	// mounted produces exactly the half-removed directory this guard exists to
	// prevent, only reached through the guard itself: RemoveAll wipes the
	// released stack, walks on, and hits EBUSY on the one still mounted.
	var failures []error
	for _, stackPath := range stackPaths {
		if releaseErr := s.releaseStackRuntime(host, stackPath); releaseErr != nil {
			failures = append(failures, releaseErr)
		}
	}
	return errors.Join(failures...)
}

func (s *Service) releaseStackRuntime(host, stackPath string) error {
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

// encryptedStacksUnder reports every encrypted stack the deletion targets,
// whether the path is a stack directory itself or an ancestor of several. It
// returns nothing when no encrypted stack is involved.
func (s *Service) encryptedStacksUnder(host, filename string) ([]string, error) {
	filename = strings.Trim(strings.TrimSpace(filename), "/")
	if filename == "" {
		return nil, nil
	}
	// The common case: the path is the stack directory.
	if enabled, err := s.encrypted.InlineEnabled(host, filename); err == nil && enabled {
		return []string{filename}, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	// Deleting an ancestor takes its encrypted stacks with it. Only one level
	// of children is examined: a deeper tree is caught by the mount check on
	// the way, and an unbounded walk here would cost more than it saves.
	stacks, err := s.ListStacks(host)
	if err != nil {
		return nil, nil
	}
	prefix := filename + "/"
	var encrypted []string
	for _, stack := range stacks {
		if stack.Path != filename && !strings.HasPrefix(stack.Path, prefix) {
			continue
		}
		if enabled, enabledErr := s.encrypted.InlineEnabled(host, stack.Path); enabledErr == nil && enabled {
			encrypted = append(encrypted, stack.Path)
		}
	}
	return encrypted, nil
}

// runtimeDirectoryOwner reports the stack a path belongs to when that path is
// the stack's runtime directory.
func runtimeDirectoryOwner(filename string) (string, bool) {
	filename = strings.Trim(strings.TrimSpace(filename), "/")
	if filename == "" {
		return "", false
	}
	parent, last := path.Split(filename)
	if last != RuntimeDirectory {
		return "", false
	}
	return strings.Trim(parent, "/"), true
}
