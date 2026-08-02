package gitsync

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/filemode"
)

const maxPolicyTreeEntries = 5000

// BindingPolicyTreeInput carries the unsaved policy editor state. A nil rule
// slice means "use the persisted rules" while an empty slice deliberately
// previews a policy without custom rules.
type BindingPolicyTreeInput struct {
	Directory       string   `json:"directory"`
	Profile         string   `json:"profile"`
	IncludePatterns []string `json:"includePatterns"`
	ExcludePatterns []string `json:"excludePatterns"`
}

type BindingPolicyTreeEntry struct {
	Name               string `json:"name"`
	Path               string `json:"path"`
	Directory          bool   `json:"directory"`
	Origin             string `json:"origin"`
	State              string `json:"state"`
	Reason             string `json:"reason,omitempty"`
	Selectable         bool   `json:"selectable"`
	ExplicitlyIncluded bool   `json:"explicitlyIncluded"`
	ExplicitlyExcluded bool   `json:"explicitlyExcluded"`
}

type BindingPolicyTreeView struct {
	Directory string                   `json:"directory"`
	Entries   []BindingPolicyTreeEntry `json:"entries"`
	Warnings  []string                 `json:"warnings,omitempty"`
}

type policyTreeCandidate struct {
	name       string
	directory  bool
	local      bool
	repository bool
	symlink    bool
	special    bool
	mismatch   bool
}

// BindingPolicyTree lists one directory at a time and never hashes or opens a
// regular file. This keeps the policy picker responsive even when a linked
// root contains large application-data trees; subdirectories are inspected
// only when the operator expands them.
func (s *Service) BindingPolicyTree(id string, input BindingPolicyTreeInput) (BindingPolicyTreeView, error) {
	binding, err := s.store.GetBinding(id)
	if err != nil {
		return BindingPolicyTreeView{}, err
	}
	directory := strings.Trim(strings.TrimSpace(filepath.ToSlash(input.Directory)), "/")
	if directory == "." {
		directory = ""
	}
	if directory != "" {
		if err := validateRelativePath(directory, false); err != nil {
			return BindingPolicyTreeView{}, errors.New("invalid policy tree directory: " + err.Error())
		}
	}

	profile := strings.TrimSpace(input.Profile)
	if profile == "" {
		profile = binding.SyncProfile
	}
	includes := input.IncludePatterns
	if includes == nil {
		includes = splitPatternLines(binding.IncludePatterns)
	}
	excludes := input.ExcludePatterns
	if excludes == nil {
		excludes = splitPatternLines(binding.ExcludePatterns)
	}
	profile, includes, excludes, err = normalizeBindingPolicy(BindingPolicyInput{Profile: profile, IncludePatterns: includes, ExcludePatterns: excludes})
	if err != nil {
		return BindingPolicyTreeView{}, err
	}
	binding.SyncProfile = profile
	binding.IncludePatterns = strings.Join(includes, "\n")
	binding.ExcludePatterns = strings.Join(excludes, "\n")
	repositoryRow, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return BindingPolicyTreeView{}, err
	}
	policy, err := policyFromBinding(binding, repositoryRow)
	if err != nil {
		return BindingPolicyTreeView{}, err
	}
	policy = policy.withComposeDirectoryIndex()

	candidates := make(map[string]*policyTreeCandidate)
	warnings := make([]string, 0, 2)
	localRules := make([]ignoreRule, 0)
	targetFS, root, stackErr := s.resolveBindingStack(binding)
	if stackErr != nil {
		warnings = append(warnings, "Dockman files are unavailable: "+safeGitError(stackErr))
	} else {
		if rules, rulesErr := loadStackIgnoreRules(targetFS, root); rulesErr == nil {
			localRules = append(localRules, rules...)
		} else {
			warnings = append(warnings, ".dockmanignore on Dockman could not be read: "+safeGitError(rulesErr))
		}
		entries, readErr := targetFS.ReadDir(targetFS.Join(root, filepath.FromSlash(directory)))
		if readErr != nil {
			if !errors.Is(readErr, fs.ErrNotExist) {
				warnings = append(warnings, "Dockman directory could not be read: "+safeGitError(readErr))
			}
		} else if len(entries) > maxPolicyTreeEntries {
			warnings = append(warnings, "Dockman directory contains more than 5000 entries; refine the path with an advanced rule instead")
		} else {
			for _, entry := range entries {
				if !safePolicyTreeName(entry.Name()) {
					continue
				}
				candidate := ensurePolicyTreeCandidate(candidates, entry.Name())
				candidate.local = true
				candidate.directory = entry.IsDir()
				candidate.symlink = entry.Type()&os.ModeSymlink != 0
				candidate.special = !entry.IsDir() && entry.Type() != 0 && !candidate.symlink
			}
		}
	}

	repo, repoErr := s.openRepository(repositoryRow)
	if repoErr != nil {
		warnings = append(warnings, "Git files are unavailable: "+safeGitError(repoErr))
	} else if tree, treeErr := repositoryCommitTree(repo, repositoryRow.DefaultBranch); treeErr != nil {
		warnings = append(warnings, "Git tree could not be read: "+safeGitError(treeErr))
	} else if tree, treeErr = repositorySubtree(tree, binding.SubPath); treeErr != nil {
		warnings = append(warnings, "The linked Git folder does not exist yet")
	} else {
		if rules, rulesErr := loadRepositoryTreeIgnoreRules(tree); rulesErr == nil {
			localRules = append(localRules, rules...)
		} else {
			warnings = append(warnings, ".dockmanignore on Git could not be read: "+safeGitError(rulesErr))
		}
		if directory != "" {
			tree, treeErr = tree.Tree(path.Clean(directory))
		}
		if treeErr != nil {
			// A local-only directory is a valid selection source.
		} else if len(tree.Entries) > maxPolicyTreeEntries {
			warnings = append(warnings, "Git directory contains more than 5000 entries; refine the path with an advanced rule instead")
		} else {
			for _, entry := range tree.Entries {
				if !safePolicyTreeName(entry.Name) {
					continue
				}
				candidate := ensurePolicyTreeCandidate(candidates, entry.Name)
				isDirectory := entry.Mode == filemode.Dir
				if candidate.local && candidate.directory != isDirectory {
					candidate.mismatch = true
				}
				candidate.repository = true
				candidate.directory = candidate.directory || isDirectory
				candidate.symlink = candidate.symlink || entry.Mode == filemode.Symlink || entry.Mode == filemode.Submodule
			}
		}
	}

	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := candidates[names[i]], candidates[names[j]]
		if left.directory != right.directory {
			return left.directory
		}
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	view := BindingPolicyTreeView{Directory: directory, Entries: make([]BindingPolicyTreeEntry, 0, len(names)), Warnings: uniqueSortedStrings(warnings)}
	for _, name := range names {
		candidate := candidates[name]
		relative := path.Join(directory, name)
		entry := evaluatePolicyTreeEntry(policy, localRules, relative, candidate)
		view.Entries = append(view.Entries, entry)
	}
	return view, nil
}

func safePolicyTreeName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`)
}

func ensurePolicyTreeCandidate(candidates map[string]*policyTreeCandidate, name string) *policyTreeCandidate {
	if existing := candidates[name]; existing != nil {
		return existing
	}
	candidate := &policyTreeCandidate{name: name}
	candidates[name] = candidate
	return candidate
}

func evaluatePolicyTreeEntry(policy syncPolicy, localRules []ignoreRule, relative string, candidate *policyTreeCandidate) BindingPolicyTreeEntry {
	origin := "both"
	if candidate.local && !candidate.repository {
		origin = "dockman"
	} else if candidate.repository && !candidate.local {
		origin = "git"
	}
	entry := BindingPolicyTreeEntry{Name: candidate.name, Path: relative, Directory: candidate.directory, Origin: origin, Selectable: true}
	entry.ExplicitlyIncluded = policy.explicitlyIncludes(relative, candidate.directory)
	entry.ExplicitlyExcluded = matchesIgnoreRule(policy.excludes, relative, candidate.directory)
	if candidate.mismatch {
		entry.State, entry.Reason, entry.Selectable = "protected", "different file types on Dockman and Git", false
		return entry
	}
	if candidate.symlink {
		entry.State, entry.Reason, entry.Selectable = "protected", "symbolic links and submodules are never synchronized", false
		return entry
	}
	if candidate.special {
		entry.State, entry.Reason, entry.Selectable = "protected", "sockets, devices and other special files are never synchronized", false
		return entry
	}
	if shouldSkipPath(relative, candidate.directory) {
		entry.State, entry.Reason, entry.Selectable = "protected", "Dockman internal data is never synchronized", false
		return entry
	}
	if !candidate.directory && isSensitivePath(relative) {
		entry.State, entry.Reason, entry.Selectable = "protected", "sensitive files require the separate one-time confirmation", false
		return entry
	}
	if policy.protectsCompose(relative) {
		entry.State, entry.Reason, entry.Selectable = "included", "Compose manifest protected by the folder link", false
		return entry
	}
	if policy.protectsProvision(relative) {
		entry.State, entry.Reason, entry.Selectable = "included", "provisioning control file", false
		return entry
	}
	selected, traverse := policy.selectsPath(relative, candidate.directory)
	if !selected && !(candidate.directory && traverse) {
		entry.State, entry.Reason = "excluded", "outside the selected stack folders"
		return entry
	}
	if policy.exclusionApplies(relative, candidate.directory, localRules) {
		entry.State, entry.Reason = "excluded", "matched by an exclusion rule"
		return entry
	}
	if candidate.directory {
		if entry.ExplicitlyIncluded || policy.profile == syncProfileAllFiles {
			entry.State, entry.Reason = "included", "directory contents selected"
		} else {
			entry.State, entry.Reason = "mixed", "contents are selected individually by the base profile"
		}
		return entry
	}
	if policy.includesFile(relative) {
		entry.State = "included"
		if entry.ExplicitlyIncluded {
			entry.Reason = "matched by an include rule"
		} else {
			entry.Reason = "included by the base profile"
		}
	} else {
		entry.State, entry.Reason = "excluded", "file type is not included by the base profile"
	}
	return entry
}
