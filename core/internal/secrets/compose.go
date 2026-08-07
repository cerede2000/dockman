package secrets

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxComposeBytes = 4 << 20

var composeManifestNames = []string{"compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml"}

type composeSecretState struct {
	ComposeSecret
	services         map[string]struct{}
	readOnlyServices map[string]struct{}
}

// AnalyzeCompose inspects only conventional manifests at the selected stack
// root. It never scans subdirectories and therefore has no idle or tree-size
// dependent cost.
func (s *PlainFileStore) AnalyzeCompose(host, stackPath string) (ComposeAnalysis, error) {
	stackFS, root, err := s.resolveStack(host, stackPath)
	if err != nil {
		return ComposeAnalysis{}, err
	}
	result := ComposeAnalysis{Manifests: []string{}, Secrets: []ComposeSecret{}}
	states := map[string]*composeSecretState{}
	for _, manifest := range composeManifestNames {
		path := stackFS.Join(root, manifest)
		info, statErr := stackFS.Lstat(path)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return ComposeAnalysis{}, fmt.Errorf("inspect Compose manifest %s: %w", manifest, statErr)
		}
		if !info.Mode().IsRegular() || info.Size() > maxComposeBytes {
			return ComposeAnalysis{}, fmt.Errorf("Compose manifest %s is not a regular file smaller than 4 MiB", manifest)
		}
		content, readErr := stackFS.ReadFile(path)
		if readErr != nil {
			return ComposeAnalysis{}, fmt.Errorf("read Compose manifest %s: %w", manifest, readErr)
		}
		var document yaml.Node
		parseErr := yaml.Unmarshal(content, &document)
		clear(content)
		if parseErr != nil {
			return ComposeAnalysis{}, fmt.Errorf("parse Compose manifest %s: %w", manifest, parseErr)
		}
		result.Manifests = append(result.Manifests, manifest)
		collectComposeSecrets(&document, states)
	}
	for _, state := range states {
		state.Services = make([]string, 0, len(state.services))
		for service := range state.services {
			state.Services = append(state.Services, service)
		}
		sort.Strings(state.Services)
		state.ReadOnlyServices = make([]string, 0, len(state.readOnlyServices))
		for service := range state.readOnlyServices {
			state.ReadOnlyServices = append(state.ReadOnlyServices, service)
		}
		sort.Strings(state.ReadOnlyServices)
		if state.File != "" {
			clean := path.Clean(strings.TrimSpace(state.File))
			clean = strings.TrimPrefix(clean, "./")
			parts := strings.Split(clean, "/")
			switch {
			case strings.Contains(clean, "$"):
				state.Issue = "source uses variable interpolation and cannot be resolved safely"
			case path.IsAbs(clean):
				state.Issue = "valid Compose source, but outside Dockman's managed .secrets directory"
			case len(parts) == 2 && parts[0] == RuntimeDirectory && validSecretName(parts[1]):
				state.Managed = true
				state.RuntimeName = parts[1]
			case strings.HasPrefix(clean, RuntimeDirectory+"/"):
				state.Issue = "nested paths inside .secrets are not managed; use .secrets/<filename>"
			default:
				state.Issue = "valid Compose source, but outside Dockman's managed .secrets directory"
			}
			if state.Managed {
				info, statErr := stackFS.Lstat(stackFS.Join(root, RuntimeDirectory, state.RuntimeName))
				state.Exists = statErr == nil && info.Mode().IsRegular()
				if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
					state.Issue = "runtime secret cannot be inspected"
				} else if !state.Exists {
					state.Issue = "runtime secret is missing"
				}
			}
		} else if state.Environment != "" {
			state.RuntimeName = strings.TrimSpace(state.Environment)
			if !inlineEnvironmentNamePattern.MatchString(state.RuntimeName) {
				state.Issue = "environment source is not a valid environment variable name"
			} else if len(state.ReadOnlyServices) > 0 {
				state.Issue = fmt.Sprintf("Docker Compose cannot mount an environment-backed secret into read-only service(s): %s; use direct ${%s}, disable read_only, or keep a materialized file source", strings.Join(state.ReadOnlyServices, ", "), state.RuntimeName)
			}
		} else if !state.External {
			state.Issue = "no file source is declared"
		}
		result.Secrets = append(result.Secrets, state.ComposeSecret)
	}
	sort.Slice(result.Secrets, func(i, j int) bool {
		return strings.ToLower(result.Secrets[i].Name) < strings.ToLower(result.Secrets[j].Name)
	})
	return result, nil
}

func collectComposeSecrets(document *yaml.Node, states map[string]*composeSecretState) {
	root := document
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return
	}
	if declarations := mappingValue(root, "secrets"); declarations != nil && declarations.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(declarations.Content); i += 2 {
			name, definition := declarations.Content[i].Value, declarations.Content[i+1]
			state := ensureComposeSecret(states, name)
			if definition.Kind == yaml.MappingNode {
				if file := mappingValue(definition, "file"); file != nil && file.Kind == yaml.ScalarNode {
					state.File = file.Value
				}
				if environment := mappingValue(definition, "environment"); environment != nil && environment.Kind == yaml.ScalarNode {
					state.Environment = environment.Value
				}
				if external := mappingValue(definition, "external"); external != nil {
					state.External = external.Value == "true" || external.Kind == yaml.MappingNode
				}
			}
		}
	}
	services := mappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		serviceName, definition := services.Content[i].Value, services.Content[i+1]
		readOnly := false
		if value := mappingValue(definition, "read_only"); value != nil && value.Kind == yaml.ScalarNode {
			readOnly = strings.EqualFold(strings.TrimSpace(value.Value), "true")
		}
		used := mappingValue(definition, "secrets")
		if used == nil || used.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range used.Content {
			name := ""
			if item.Kind == yaml.ScalarNode {
				name = item.Value
			} else if item.Kind == yaml.MappingNode {
				if source := mappingValue(item, "source"); source != nil {
					name = source.Value
				}
			}
			if name != "" {
				state := ensureComposeSecret(states, name)
				state.services[serviceName] = struct{}{}
				if readOnly {
					state.readOnlyServices[serviceName] = struct{}{}
				}
			}
		}
	}
}

func ensureComposeSecret(states map[string]*composeSecretState, name string) *composeSecretState {
	if state, ok := states[name]; ok {
		return state
	}
	state := &composeSecretState{
		ComposeSecret:    ComposeSecret{Name: name, Services: []string{}, ReadOnlyServices: []string{}},
		services:         map[string]struct{}{},
		readOnlyServices: map[string]struct{}{},
	}
	states[name] = state
	return state
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
