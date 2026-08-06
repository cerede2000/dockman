package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RA341/dockman/internal/host/filesystem"
	"gopkg.in/yaml.v3"
)

const (
	SOPSInlineMarkerFile   = ".dockman-sops-inline"
	SOPSRecoveryScriptFile = "compose-sops.sh"
)

var inlineEnvironmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// InlineEnabled is deliberately represented inside the stack rather than in
// Dockman's database. The encrypted source and recovery script therefore keep
// their meaning after a Dockman loss or a host rebuild.
func (p *SOPSProvider) InlineEnabled(host, stackPath string) (bool, error) {
	stackFS, root, err := p.resolveStack(host, stackPath)
	if err != nil {
		return false, err
	}
	return inlineMarkerExists(stackFS, root)
}

func inlineMarkerExists(stackFS filesystem.FileSystem, root string) (bool, error) {
	info, err := stackFS.Lstat(stackFS.Join(root, SOPSInlineMarkerFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect inline SOPS policy: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 128 {
		return false, errors.New("inline SOPS policy marker is invalid")
	}
	value, err := stackFS.ReadFile(stackFS.Join(root, SOPSInlineMarkerFile))
	if err != nil {
		return false, fmt.Errorf("read inline SOPS policy: %w", err)
	}
	if strings.TrimSpace(string(value)) != "version=1" {
		return false, errors.New("inline SOPS policy marker has an unsupported version")
	}
	return true, nil
}

// ComposeEnvironment decrypts only when an action targets an explicitly opted
// in stack. There is no polling, plaintext file or cross-action cache.
func (p *SOPSProvider) ComposeEnvironment(parent context.Context, host string, stackFS filesystem.FileSystem, composeRelpath string) ([]string, error) {
	root := filepath.Dir(strings.TrimPrefix(composeRelpath, "/"))
	enabled, err := inlineMarkerExists(stackFS, root)
	if err != nil || !enabled {
		return nil, err
	}
	if err = p.requireAvailable(); err != nil {
		return nil, err
	}
	values, err := p.readValues(parent, stackFS, root)
	if err != nil {
		return nil, err
	}
	volatile, err := p.volatileRuntimeAvailable(parent, host, stackFS, root)
	if err != nil {
		return nil, err
	}
	if volatile {
		if err = syncVolatileRuntime(stackFS, root, values); err != nil {
			return nil, err
		}
	} else if composeUsesManagedFileSecrets(stackFS, composeRelpath) {
		return nil, errors.New("volatile file secrets are not mounted; run sudo systemctl restart dockman-secrets-host.service and verify the Dockman stack bind uses rslave propagation")
	}
	names := sortedValueNames(values)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		if !inlineEnvironmentNamePattern.MatchString(name) {
			// File-only secret names may contain dots or dashes. They remain in
			// tmpfs and must not be exported to the Compose process.
			continue
		}
		if strings.IndexByte(values[name], 0) >= 0 {
			return nil, fmt.Errorf("inline SOPS value %q contains a NUL byte", name)
		}
		if len(values[name]) > MaxSecretBytes {
			return nil, fmt.Errorf("inline SOPS value %q exceeds the 1 MiB limit", name)
		}
		environment = append(environment, name+"="+values[name])
	}
	return environment, nil
}

func (p *SOPSProvider) EnableInline(parent context.Context, host, stackPath, composeFile string) (SOPSResult, error) {
	if err := p.requireAvailable(); err != nil {
		return SOPSResult{}, err
	}
	if enabled, err := p.InlineEnabled(host, stackPath); err != nil {
		return SOPSResult{}, err
	} else if enabled {
		return SOPSResult{}, errors.New("inline SOPS mode is already enabled")
	}
	composeFile = strings.TrimSpace(composeFile)
	if composeFile == "" {
		composeFile = "compose.yml"
	}
	if filepath.Base(composeFile) != composeFile || !isComposeManifestName(composeFile) {
		return SOPSResult{}, errors.New("select a conventional Compose manifest from the stack root")
	}
	stackFS, root, err := p.resolveStack(host, stackPath)
	if err != nil {
		return SOPSResult{}, err
	}
	if _, scriptErr := stackFS.Lstat(stackFS.Join(root, SOPSRecoveryScriptFile)); scriptErr == nil {
		return SOPSResult{}, fmt.Errorf("refusing to overwrite existing %s", SOPSRecoveryScriptFile)
	} else if !errors.Is(scriptErr, fs.ErrNotExist) {
		return SOPSResult{}, fmt.Errorf("inspect recovery script destination: %w", scriptErr)
	}
	analysis, err := p.runtime.AnalyzeCompose(host, stackPath)
	if err != nil {
		return SOPSResult{}, fmt.Errorf("analyze Compose before enabling inline mode: %w", err)
	}
	requiresRuntimeFiles := false
	for _, reference := range analysis.Secrets {
		if reference.Managed && len(reference.Services) > 0 {
			requiresRuntimeFiles = true
		}
	}

	// Export and verify ciphertext before removing any runtime plaintext.
	result, err := p.Export(parent, host, stackPath)
	if err != nil {
		return SOPSResult{}, err
	}
	p.operation.Lock()
	defer p.operation.Unlock()
	values, err := p.readValues(parent, stackFS, root)
	if err != nil {
		return SOPSResult{}, fmt.Errorf("verify inline environment: %w", err)
	}
	for name, value := range values {
		if !validSecretName(name) {
			return SOPSResult{}, fmt.Errorf("secret %q is not a valid file or environment secret name", name)
		}
		if strings.IndexByte(value, 0) >= 0 {
			return SOPSResult{}, fmt.Errorf("secret %q contains a NUL byte and cannot be injected", name)
		}
	}
	for _, reference := range analysis.Secrets {
		if reference.Environment == "" || len(reference.Services) == 0 {
			continue
		}
		name := strings.TrimSpace(reference.Environment)
		if len(reference.ReadOnlyServices) > 0 {
			return SOPSResult{}, fmt.Errorf("Compose secret %q uses environment source %q with read-only service(s) %s; Docker Compose supports only file sources there, so use file: ./.secrets/%s with the autonomous tmpfs runtime", reference.Name, name, strings.Join(reference.ReadOnlyServices, ", "), name)
		}
		if !inlineEnvironmentNamePattern.MatchString(name) {
			return SOPSResult{}, fmt.Errorf("Compose secret %q uses invalid environment source %q", reference.Name, reference.Environment)
		}
		if _, exists := values[name]; !exists {
			return SOPSResult{}, fmt.Errorf("Compose secret %q requires encrypted value %q; create that secret before enabling inline mode", reference.Name, name)
		}
	}
	if err = writeAtomic(stackFS, stackFS.Join(root, SOPSRecoveryScriptFile), []byte(recoveryScript(composeFile, requiresRuntimeFiles)), 0o700); err != nil {
		return SOPSResult{}, fmt.Errorf("write recovery script: %w", err)
	}
	// This intentionally removes bounded plaintext history as well. Keeping it
	// would violate the user's encrypted-at-rest choice.
	if err = stackFS.RemoveAll(stackFS.Join(root, RuntimeDirectory)); err != nil {
		return SOPSResult{}, fmt.Errorf("remove plaintext runtime secret directory: %w", err)
	}
	if err = writeAtomic(stackFS, stackFS.Join(root, SOPSInlineMarkerFile), []byte("version=1\n"), 0o600); err != nil {
		return SOPSResult{}, fmt.Errorf("activate inline SOPS policy: %w", err)
	}
	return result, nil
}

func (p *SOPSProvider) DisableInline(parent context.Context, host, stackPath string) (SOPSResult, error) {
	result, err := p.Materialize(parent, host, stackPath)
	if err != nil {
		return SOPSResult{}, err
	}
	p.operation.Lock()
	defer p.operation.Unlock()
	stackFS, root, err := p.resolveStack(host, stackPath)
	if err != nil {
		return SOPSResult{}, err
	}
	for _, name := range []string{SOPSInlineMarkerFile, SOPSRecoveryScriptFile} {
		if removeErr := stackFS.RemoveAll(stackFS.Join(root, name)); removeErr != nil {
			return SOPSResult{}, fmt.Errorf("disable inline SOPS policy: %w", removeErr)
		}
	}
	return result, nil
}

func (p *SOPSProvider) ListInline(parent context.Context, host, stackPath string) ([]Metadata, error) {
	stackFS, root, values, err := p.inlineValues(parent, host, stackPath)
	if err != nil {
		return nil, err
	}
	info, err := stackFS.Stat(stackFS.Join(root, SOPSSourceFile))
	if err != nil {
		return nil, err
	}
	names := sortedValueNames(values)
	items := make([]Metadata, 0, len(names))
	for _, name := range names {
		items = append(items, Metadata{Name: name, Size: int64(len(values[name])), ModifiedAt: info.ModTime()})
	}
	return items, nil
}

func (p *SOPSProvider) ReadInline(parent context.Context, host, stackPath, name string) ([]byte, error) {
	if !validSecretName(name) {
		return nil, ErrInvalidName
	}
	_, _, values, err := p.inlineValues(parent, host, stackPath)
	if err != nil {
		return nil, err
	}
	value, exists := values[name]
	if !exists {
		return nil, fs.ErrNotExist
	}
	return []byte(value), nil
}

func (p *SOPSProvider) WriteInline(parent context.Context, host, stackPath, name string, value []byte) (Metadata, error) {
	if !validSecretName(name) {
		return Metadata{}, ErrInvalidName
	}
	if len(value) > MaxSecretBytes {
		return Metadata{}, ErrSecretTooLarge
	}
	if !utf8.Valid(value) || strings.IndexByte(string(value), 0) >= 0 {
		return Metadata{}, errors.New("inline secret must be UTF-8 text without NUL bytes")
	}
	p.operation.Lock()
	defer p.operation.Unlock()
	stackFS, root, values, err := p.inlineValuesUnlocked(parent, host, stackPath)
	if err != nil {
		return Metadata{}, err
	}
	values[name] = string(value)
	if err = p.writeValues(parent, stackFS, root, values); err != nil {
		return Metadata{}, err
	}
	if volatile, checkErr := p.volatileRuntimeAvailable(parent, host, stackFS, root); checkErr != nil {
		return Metadata{}, checkErr
	} else if volatile {
		if err = syncVolatileRuntime(stackFS, root, values); err != nil {
			return Metadata{}, fmt.Errorf("ciphertext updated but volatile runtime refresh failed: %w", err)
		}
	}
	return Metadata{Name: name, Size: int64(len(value)), ModifiedAt: time.Now().UTC()}, nil
}

func (p *SOPSProvider) DeleteInline(parent context.Context, host, stackPath, name string) error {
	if !validSecretName(name) {
		return ErrInvalidName
	}
	analysis, err := p.runtime.AnalyzeCompose(host, stackPath)
	if err != nil {
		return fmt.Errorf("analyze Compose before deleting inline secret: %w", err)
	}
	for _, reference := range analysis.Secrets {
		if reference.Managed && reference.RuntimeName == name && len(reference.Services) > 0 {
			return fmt.Errorf("encrypted secret %q supplies Compose file secret %q used by %s; remove that Compose reference before deleting it", name, reference.Name, strings.Join(reference.Services, ", "))
		}
		if strings.TrimSpace(reference.Environment) == name && len(reference.Services) > 0 {
			return fmt.Errorf("inline secret %q supplies Compose file secret %q used by %s; remove that Compose reference before deleting it", name, reference.Name, strings.Join(reference.Services, ", "))
		}
	}
	p.operation.Lock()
	defer p.operation.Unlock()
	stackFS, root, values, err := p.inlineValuesUnlocked(parent, host, stackPath)
	if err != nil {
		return err
	}
	if _, exists := values[name]; !exists {
		return fs.ErrNotExist
	}
	delete(values, name)
	if err = p.writeValues(parent, stackFS, root, values); err != nil {
		return err
	}
	if volatile, checkErr := p.volatileRuntimeAvailable(parent, host, stackFS, root); checkErr != nil {
		return checkErr
	} else if volatile {
		if err = syncVolatileRuntime(stackFS, root, values); err != nil {
			return fmt.Errorf("ciphertext updated but volatile runtime refresh failed: %w", err)
		}
	}
	return nil
}

func (p *SOPSProvider) volatileRuntimeAvailable(ctx context.Context, host string, stackFS filesystem.FileSystem, root string) (bool, error) {
	marker := stackFS.Join(root, RuntimeDirectory, HostRuntimeMarkerFile)
	info, err := stackFS.Lstat(marker)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect volatile secret runtime: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 32 {
		return false, errors.New("volatile secret runtime marker is invalid")
	}
	value, err := stackFS.ReadFile(marker)
	if err != nil {
		return false, fmt.Errorf("read volatile secret runtime marker: %w", err)
	}
	if strings.TrimSpace(string(value)) != "version=1" {
		return false, errors.New("volatile secret runtime marker has an unsupported version")
	}
	absolute, err := stackFS.Abs(stackFS.Join(root, RuntimeDirectory))
	if err != nil {
		return false, fmt.Errorf("resolve volatile secret runtime: %w", err)
	}
	if p.verifyRuntime != nil {
		return p.verifyRuntime(ctx, host, absolute)
	}
	return IsManagedHostRuntimeMount(absolute)
}

func syncVolatileRuntime(stackFS filesystem.FileSystem, root string, values map[string]string) error {
	directory := stackFS.Join(root, RuntimeDirectory)
	entries, err := stackFS.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("list volatile secret runtime: %w", err)
	}
	for _, entry := range entries {
		if !validSecretName(entry.Name()) {
			continue
		}
		if _, keep := values[entry.Name()]; keep {
			continue
		}
		info, statErr := stackFS.Lstat(stackFS.Join(directory, entry.Name()))
		if statErr != nil {
			return statErr
		}
		if info.Mode().IsRegular() {
			if err = stackFS.RemoveAll(stackFS.Join(directory, entry.Name())); err != nil {
				return fmt.Errorf("remove stale volatile secret %q: %w", entry.Name(), err)
			}
		}
	}
	for _, name := range sortedValueNames(values) {
		value := []byte(values[name])
		err = writeAtomic(stackFS, stackFS.Join(directory, name), value, 0o444)
		clear(value)
		if err != nil {
			return fmt.Errorf("refresh volatile secret %q: %w", name, err)
		}
	}
	return nil
}

func composeUsesManagedFileSecrets(stackFS filesystem.FileSystem, composeRelpath string) bool {
	info, err := stackFS.Lstat(composeRelpath)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxComposeBytes {
		return false
	}
	value, err := stackFS.ReadFile(composeRelpath)
	if err != nil {
		return false
	}
	defer clear(value)
	var document yaml.Node
	if yaml.Unmarshal(value, &document) != nil {
		return false
	}
	states := map[string]*composeSecretState{}
	collectComposeSecrets(&document, states)
	for _, state := range states {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(state.File)))
		clean = strings.TrimPrefix(clean, "./")
		if len(state.services) > 0 && strings.HasPrefix(clean, RuntimeDirectory+"/") {
			return true
		}
	}
	return false
}

func (p *SOPSProvider) inlineValues(parent context.Context, host, stackPath string) (filesystem.FileSystem, string, map[string]string, error) {
	p.operation.Lock()
	defer p.operation.Unlock()
	return p.inlineValuesUnlocked(parent, host, stackPath)
}

func (p *SOPSProvider) inlineValuesUnlocked(parent context.Context, host, stackPath string) (filesystem.FileSystem, string, map[string]string, error) {
	if err := p.requireAvailable(); err != nil {
		return nil, "", nil, err
	}
	stackFS, root, err := p.resolveStack(host, stackPath)
	if err != nil {
		return nil, "", nil, err
	}
	enabled, err := inlineMarkerExists(stackFS, root)
	if err != nil {
		return nil, "", nil, err
	}
	if !enabled {
		return nil, "", nil, errors.New("inline SOPS mode is not enabled for this stack")
	}
	values, err := p.readValues(parent, stackFS, root)
	return stackFS, root, values, err
}

func (p *SOPSProvider) readValues(parent context.Context, stackFS filesystem.FileSystem, root string) (map[string]string, error) {
	source := stackFS.Join(root, SOPSSourceFile)
	info, err := stackFS.Lstat(source)
	if err != nil {
		return nil, fmt.Errorf("inspect encrypted secret source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSOPSSourceBytes {
		return nil, errors.New("encrypted secret source is not a bounded regular file")
	}
	encrypted, err := stackFS.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("read encrypted secret source: %w", err)
	}
	defer clear(encrypted)
	ctx, cancel := context.WithTimeout(parent, sopsTimeout)
	defer cancel()
	return p.decrypt(ctx, encrypted)
}

func (p *SOPSProvider) writeValues(parent context.Context, stackFS filesystem.FileSystem, root string, values map[string]string) error {
	plain, err := json.Marshal(values)
	if err != nil {
		return errors.New("encode inline secrets for SOPS")
	}
	defer clear(plain)
	ctx, cancel := context.WithTimeout(parent, sopsTimeout)
	defer cancel()
	encrypted, err := p.runner.Run(ctx, p.binary, []string{
		"encrypt", "--age", p.recipient, "--input-type", "json", "--output-type", "yaml",
		"--filename-override", SOPSSourceFile, "/dev/stdin",
	}, p.environment(), plain)
	if err != nil {
		return err
	}
	defer clear(encrypted)
	verified, err := p.decrypt(ctx, encrypted)
	if err != nil {
		return fmt.Errorf("verify encrypted inline source: %w", err)
	}
	if len(verified) != len(values) {
		return errors.New("verify encrypted inline source: key count changed")
	}
	for name, expected := range values {
		if verified[name] != expected {
			return fmt.Errorf("verify encrypted inline source: value %q changed", name)
		}
	}
	return writeAtomic(stackFS, stackFS.Join(root, SOPSSourceFile), encrypted, 0o600)
}

func sortedValueNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isComposeManifestName(name string) bool {
	switch strings.ToLower(name) {
	case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
		return true
	default:
		return false
	}
}

func recoveryScript(composeFile string, requiresRuntimeFiles bool) string {
	runtimeCheck := ""
	if requiresRuntimeFiles {
		runtimeCheck = `
if ! awk -v path="$PWD/.secrets" '$5 == path { found=1 } END { exit !found }' /proc/self/mountinfo; then
  echo "volatile file secrets are not mounted; run: sudo systemctl restart dockman-secrets-host.service" >&2
  exit 78
fi
`
	}
	return `#!/bin/sh
set -eu

# Generated by Dockman. This script is deliberately self-contained: recovery
# needs only Docker Compose, SOPS, the encrypted source and the age identity.
: "${SOPS_AGE_KEY_FILE:?set SOPS_AGE_KEY_FILE to the backed-up age identity}"
command -v sops >/dev/null 2>&1 || { echo "sops is required" >&2; exit 127; }
command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 127; }

cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
` + runtimeCheck + `
action="${1:-up}"
case "$action" in
  up) command='docker compose -f ` + composeFile + ` up -d --remove-orphans' ;;
  down) command='docker compose -f ` + composeFile + ` down' ;;
  start|stop|restart|pull|config|ps) command="docker compose -f ` + composeFile + ` $action" ;;
  shell) command='sh' ;;
  *) echo "usage: $0 {up|down|start|stop|restart|pull|config|ps|shell}" >&2; exit 64 ;;
esac
exec sops exec-env ` + SOPSSourceFile + ` "$command"
`
}
