package secrets

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/RA341/dockman/internal/host/filesystem"
	"gopkg.in/yaml.v3"
)

const maxGlobalSecretAssignments = 50

type CatalogAssignment struct {
	StackPath string   `json:"stackPath"`
	Alias     string   `json:"alias"`
	Manifests []string `json:"manifests"`
	Mode      string   `json:"mode"`
}

type CatalogSecret struct {
	Name        string              `json:"name"`
	Assignments []CatalogAssignment `json:"assignments"`
}

type CatalogStack struct {
	StackOption
	Mode string `json:"mode"`
}

type Catalog struct {
	Secrets []CatalogSecret `json:"secrets"`
	Stacks  []CatalogStack  `json:"stacks"`
}

// ListCatalog builds a bounded, request-driven index. Encrypted stack names
// are read from SOPS YAML keys without decrypting values or launching SOPS.
func (s *Service) ListCatalog(ctx context.Context, host string) (Catalog, error) {
	options, err := s.ListStacks(host)
	if err != nil {
		return Catalog{}, err
	}
	byName := make(map[string][]CatalogAssignment)
	stacks := make([]CatalogStack, 0, len(options))
	for _, option := range options {
		mode := "migration"
		var items []Metadata
		if s.encrypted != nil {
			enabled, inlineErr := s.encrypted.InlineEnabled(host, option.Path)
			if inlineErr != nil {
				continue
			}
			if enabled {
				mode = "encrypted"
				items, err = s.encrypted.ListEncryptedMetadata(host, option.Path)
			} else {
				items, err = s.runtime.List(host, option.Path)
			}
		} else {
			items, err = s.runtime.List(host, option.Path)
		}
		// Keep every discovered Compose stack selectable even when one stack's
		// secret catalog is temporarily unreadable. A damaged or inaccessible
		// stack must not hide the other stacks or prevent a clean stack from
		// being initialized.
		stacks = append(stacks, CatalogStack{StackOption: option, Mode: mode})
		if err != nil {
			continue
		}
		for _, item := range items {
			byName[item.Name] = append(byName[item.Name], CatalogAssignment{
				StackPath: option.Path, Alias: option.Alias, Manifests: option.Manifests, Mode: mode,
			})
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	result := Catalog{Secrets: make([]CatalogSecret, 0, len(names)), Stacks: stacks}
	for _, name := range names {
		assignments := byName[name]
		sort.Slice(assignments, func(i, j int) bool {
			return strings.ToLower(assignments[i].StackPath) < strings.ToLower(assignments[j].StackPath)
		})
		result.Secrets = append(result.Secrets, CatalogSecret{Name: name, Assignments: assignments})
	}
	return result, nil
}

func (p *SOPSProvider) ListEncryptedMetadata(host, stackPath string) ([]Metadata, error) {
	stackFS, root, err := p.resolveStack(host, stackPath)
	if err != nil {
		return nil, err
	}
	if enabled, markerErr := inlineMarkerExists(stackFS, root); markerErr != nil || !enabled {
		if markerErr != nil {
			return nil, markerErr
		}
		return nil, errors.New("encrypted runtime is not enabled")
	}
	path := stackFS.Join(root, SOPSSourceFile)
	info, err := stackFS.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSOPSSourceBytes {
		return nil, errors.New("encrypted secret source is not a bounded regular file")
	}
	value, err := stackFS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read encrypted secret catalog: %w", err)
	}
	defer clear(value)
	var document yaml.Node
	if err = yaml.Unmarshal(value, &document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("encrypted secret source is not a YAML mapping")
	}
	mapping := document.Content[0]
	items := make([]Metadata, 0, len(mapping.Content)/2)
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		name := mapping.Content[index].Value
		if name == "sops" {
			continue
		}
		if !validSecretName(name) {
			return nil, fmt.Errorf("encrypted secret source contains invalid name %q", name)
		}
		items = append(items, Metadata{Name: name, ModifiedAt: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (s *Service) AssignEncrypted(ctx context.Context, host, name string, value []byte, stackPaths []string) ([]CatalogAssignment, error) {
	if s.encrypted == nil {
		return nil, ErrSOPSUnavailable
	}
	return s.encrypted.AssignEncrypted(ctx, host, name, value, stackPaths)
}

func (p *SOPSProvider) AssignEncrypted(parent context.Context, host, name string, value []byte, stackPaths []string) ([]CatalogAssignment, error) {
	if !validSecretName(name) {
		return nil, ErrInvalidName
	}
	if len(value) > MaxSecretBytes {
		return nil, ErrSecretTooLarge
	}
	if !utf8.Valid(value) || strings.IndexByte(string(value), 0) >= 0 {
		return nil, errors.New("global secret must be UTF-8 text without NUL bytes")
	}
	if len(stackPaths) == 0 || len(stackPaths) > maxGlobalSecretAssignments {
		return nil, fmt.Errorf("select between 1 and %d encrypted stacks", maxGlobalSecretAssignments)
	}
	if err := p.requireAvailable(); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(stackPaths))
	seen := map[string]struct{}{}
	for _, path := range stackPaths {
		path = strings.TrimSpace(path)
		if path == "" {
			return nil, ErrInvalidStackPath
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)

	type target struct {
		path   string
		values map[string]string
		before map[string]string
	}
	targets := make([]target, 0, len(paths))
	p.operation.Lock()
	defer p.operation.Unlock()
	for _, path := range paths {
		stackFS, root, values, err := p.inlineValuesUnlocked(parent, host, path)
		if err != nil {
			return nil, fmt.Errorf("prepare encrypted stack %s: %w", path, err)
		}
		before := make(map[string]string, len(values))
		for key, current := range values {
			before[key] = current
		}
		values[name] = string(value)
		targets = append(targets, target{path: path, values: values, before: before})
		_ = stackFS
		_ = root
	}
	written := 0
	for index, current := range targets {
		stackFS, root, err := p.resolveStack(host, current.path)
		if err == nil {
			err = p.writeValues(parent, stackFS, root, current.values)
		}
		if err != nil {
			for rollback := written - 1; rollback >= 0; rollback-- {
				rollbackFS, rollbackRoot, resolveErr := p.resolveStack(host, targets[rollback].path)
				if resolveErr == nil {
					_ = p.writeValues(context.Background(), rollbackFS, rollbackRoot, targets[rollback].before)
				}
			}
			return nil, fmt.Errorf("assign encrypted secret to %s: %w; completed assignments were rolled back", current.path, err)
		}
		written = index + 1
	}
	assignments := make([]CatalogAssignment, 0, len(targets))
	// One reconciliation request for the whole operation, not one per stack.
	// Every stack of a host writes the same request file, so a bulk assignment
	// used to emit one inotify event per target: the host unit was started
	// dozens of times over, each run re-materializing every stack, and the
	// systemd start limit turned that burst into a lasting failure of the
	// watch. A single request converges to the same state.
	var reconcile filesystem.FileSystem
	for _, current := range targets {
		stackFS, root, err := p.resolveStack(host, current.path)
		if err != nil {
			continue
		}
		if volatile, checkErr := p.volatileRuntimeAvailable(parent, host, stackFS, root); checkErr == nil && volatile {
			_ = syncVolatileRuntime(stackFS, root, current.values)
		} else if reconcile == nil {
			reconcile = stackFS
		}
		assignments = append(assignments, CatalogAssignment{StackPath: current.path, Mode: "encrypted"})
	}
	if reconcile != nil {
		_ = requestHostRuntimeReconcile(reconcile)
	}
	return assignments, nil
}
