package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RA341/dockman/internal/host/filesystem"
)

const (
	maxStackDiscoveryDirectories = 1000
	maxStackDiscoveryResults     = 500
	maxStackDiscoveryDepth       = 8
)

type stackDiscoveryDirectory struct {
	path     string
	relative string
	depth    int
}

// ListStacks performs one bounded, breadth-first catalog refresh on demand.
// It is intentionally not cached or scheduled, so changing hosts cannot leak
// paths and an idle Dockman pays no CPU or memory cost for this convenience.
func (s *PlainFileStore) ListStacks(host string) ([]StackOption, error) {
	aliases := []string{"compose"}
	if s.aliases != nil {
		listed, err := s.aliases(host)
		if err != nil {
			return nil, fmt.Errorf("list stack aliases: %w", err)
		}
		aliases = uniqueAliases(listed)
	}
	result := make([]StackOption, 0)
	for _, alias := range aliases {
		stackFS, root, err := s.resolve(host, alias)
		if err != nil {
			continue
		}
		result = append(result, discoverAliasStacks(stackFS, root, alias, maxStackDiscoveryResults-len(result))...)
		if len(result) >= maxStackDiscoveryResults {
			break
		}
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Path) < strings.ToLower(result[j].Path) })
	return result, nil
}

func discoverAliasStacks(stackFS filesystem.FileSystem, root, alias string, remaining int) []StackOption {
	if remaining <= 0 {
		return nil
	}
	queue := []stackDiscoveryDirectory{{path: root}}
	visited := 0
	result := make([]StackOption, 0)
	for len(queue) > 0 && visited < maxStackDiscoveryDirectories && len(result) < remaining {
		current := queue[0]
		queue = queue[1:]
		if current.depth > maxStackDiscoveryDepth {
			continue
		}
		visited++
		entries, err := stackFS.ReadDir(current.path)
		if err != nil {
			continue
		}
		manifests := make([]string, 0, 1)
		children := make([]stackDiscoveryDirectory, 0)
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				if skipStackDiscoveryDirectory(entry.Name()) {
					continue
				}
				children = append(children, stackDiscoveryDirectory{
					path: stackFS.Join(current.path, entry.Name()), relative: filepath.Join(current.relative, entry.Name()), depth: current.depth + 1,
				})
				continue
			}
			if isComposeManifest(entry.Name()) {
				manifests = append(manifests, entry.Name())
			}
		}
		if len(manifests) > 0 {
			sort.Strings(manifests)
			path := alias
			if clean := filepath.ToSlash(filepath.Clean(current.relative)); clean != "." && clean != "" {
				path += "/" + strings.Trim(clean, "/")
			}
			result = append(result, StackOption{Path: path, Alias: alias, Manifests: manifests})
		}
		budget := maxStackDiscoveryDirectories - visited - len(queue)
		if budget > len(children) {
			budget = len(children)
		}
		if budget > 0 {
			queue = append(queue, children[:budget]...)
		}
	}
	return result
}

func uniqueAliases(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Trim(strings.TrimSpace(value), "/")
		if value == "" || strings.ContainsAny(value, `/\\`) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func skipStackDiscoveryDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".secrets", ".dockman-backups", "node_modules", "vendor", "__pycache__":
		return true
	default:
		return strings.HasPrefix(name, ".dockman-provision-staging-")
	}
}

func isComposeManifest(name string) bool {
	switch strings.ToLower(name) {
	case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
		return true
	default:
		return false
	}
}
