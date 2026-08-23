package gitsync

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// maxAutoExcludedPaths bounds what one Folder Link may hold out. A link that
// reaches it has a systemic permission problem, not a handful of protected
// files, and silently swallowing more would hide it.
const maxAutoExcludedPaths = 256

// A path the host's ACLs keep Dockman from reading is a fact about the host,
// not a fault Dockman can repair. Reported once, it is then held out of both
// inventories: the stack stays green, the link stays synchronized, and the file
// is never transferred in either direction - Git content must never be written
// over local content nobody managed to read.
//
// The exclusion is re-checked on every cycle, so fixing the ACL is enough to
// bring the path back. It is deliberately NOT dropped when the path disappears:
// an ignored file that someone deletes must stay ignored, not be restored from
// Git behind their back.
func mergeUnreadablePaths(current, discovered []string) ([]string, []string, bool) {
	seen := make(map[string]struct{}, len(current))
	merged := make([]string, 0, len(current)+len(discovered))
	for _, path := range current {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		merged = append(merged, path)
	}
	added := make([]string, 0, len(discovered))
	for _, raw := range discovered {
		path := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw)))), "/")
		if path == "" || path == "." {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		if len(merged) >= maxAutoExcludedPaths {
			log.Warn().Str("path", path).Int("limit", maxAutoExcludedPaths).
				Msg("Folder Link already holds out the maximum number of unreadable paths; this one keeps being reported")
			break
		}
		seen[path] = struct{}{}
		merged = append(merged, path)
		added = append(added, path)
	}
	if len(added) == 0 {
		return current, nil, false
	}
	sort.Strings(merged)
	sort.Strings(added)
	return merged, added, true
}

// recordUnreadablePaths holds newly discovered unreadable paths out of the next
// cycles and forgets their baseline entries: a path that is no longer part of
// the inventory must not be able to come back later as a local deletion
// awaiting a decision.
func (s *Service) recordUnreadablePaths(binding StackBinding, discovered []string) (StackBinding, bool) {
	if len(discovered) == 0 {
		return binding, false
	}
	merged, added, changed := mergeUnreadablePaths(splitPatternLines(binding.AutoExcludedPaths), discovered)
	if !changed {
		return binding, false
	}
	binding.AutoExcludedPaths = strings.Join(merged, "\n")
	if err := s.store.SaveBinding(&binding); err != nil {
		log.Warn().Err(err).Msg("Could not hold unreadable stack paths out of Git synchronization")
		return binding, false
	}
	if baseline, err := s.store.BindingBaseline(binding.UUID); err == nil && len(baseline) > 0 {
		pruned := false
		for path := range baseline {
			if pathIsHeldOut(path, added) {
				delete(baseline, path)
				pruned = true
			}
		}
		if pruned {
			_ = s.store.ReplaceBindingBaseline(binding.UUID, baseline)
		}
	}
	log.Info().Str("binding", binding.UUID).Strs("paths", added).
		Msg("Stack paths Dockman cannot read are now held out of Git synchronization")
	return binding, true
}

func pathIsHeldOut(path string, heldOut []string) bool {
	path = strings.Trim(filepath.ToSlash(path), "/")
	for _, blocked := range heldOut {
		if path == blocked || strings.HasPrefix(path, blocked+"/") {
			return true
		}
	}
	return false
}

// refreshUnreadableExclusions drops the paths that became readable again, so a
// corrected ACL is all it takes. Costs nothing while the list is empty, which
// is the normal case; otherwise one open attempt per held-out path per cycle.
//
// A path that no longer exists keeps its exclusion on purpose. Dropping it
// would put it back in the inventory, and the next import would restore from
// Git a file the operator deliberately removed.
func (s *Service) refreshUnreadableExclusions(binding StackBinding) StackBinding {
	held := splitPatternLines(binding.AutoExcludedPaths)
	if len(held) == 0 {
		return binding
	}
	targetFS, root, err := s.resolveBindingStack(binding)
	if err != nil {
		return binding
	}
	kept := make([]string, 0, len(held))
	recovered := make([]string, 0)
	for _, relative := range held {
		if pathIsReadable(targetFS, targetFS.Join(root, filepath.FromSlash(relative))) {
			recovered = append(recovered, relative)
			continue
		}
		kept = append(kept, relative)
	}
	if len(recovered) == 0 {
		return binding
	}
	binding.AutoExcludedPaths = strings.Join(kept, "\n")
	if err := s.store.SaveBinding(&binding); err != nil {
		log.Warn().Err(err).Msg("Could not restore Git synchronization for paths that became readable")
		return binding
	}
	log.Info().Str("binding", binding.UUID).Strs("paths", recovered).
		Msg("Stack paths are readable again and rejoin Git synchronization")
	return binding
}

func pathIsReadable(targetFS interface {
	Stat(string) (fs.FileInfo, error)
	ReadDir(string) ([]fs.DirEntry, error)
	OpenFile(string, int, fs.FileMode) (io.ReadWriteCloser, error)
}, absolute string) bool {
	info, err := targetFS.Stat(absolute)
	if err != nil {
		return false
	}
	if info.IsDir() {
		_, err = targetFS.ReadDir(absolute)
		return err == nil
	}
	handle, err := targetFS.OpenFile(absolute, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	_ = handle.Close()
	return true
}
