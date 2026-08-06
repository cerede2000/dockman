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
func (p *SOPSProvider) ComposeEnvironment(parent context.Context, _ string, stackFS filesystem.FileSystem, composeRelpath string) ([]string, error) {
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
	names := sortedValueNames(values)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		if !inlineEnvironmentNamePattern.MatchString(name) {
			return nil, fmt.Errorf("inline SOPS key %q is not a valid environment variable name", name)
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
	for _, reference := range analysis.Secrets {
		if reference.Managed {
			return SOPSResult{}, fmt.Errorf("Compose secret %q still uses %s; replace file-backed references with ${ENV_NAME} before enabling inline mode", reference.Name, reference.File)
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
		if !inlineEnvironmentNamePattern.MatchString(name) {
			return SOPSResult{}, fmt.Errorf("secret %q cannot be used inline; use an environment name such as API_TOKEN", name)
		}
		if strings.IndexByte(value, 0) >= 0 {
			return SOPSResult{}, fmt.Errorf("secret %q contains a NUL byte and cannot be injected", name)
		}
	}
	if err = writeAtomic(stackFS, stackFS.Join(root, SOPSRecoveryScriptFile), []byte(recoveryScript(composeFile)), 0o700); err != nil {
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
	if !inlineEnvironmentNamePattern.MatchString(name) {
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
	if !inlineEnvironmentNamePattern.MatchString(name) {
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
	return Metadata{Name: name, Size: int64(len(value)), ModifiedAt: time.Now().UTC()}, nil
}

func (p *SOPSProvider) DeleteInline(parent context.Context, host, stackPath, name string) error {
	if !inlineEnvironmentNamePattern.MatchString(name) {
		return ErrInvalidName
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
	return p.writeValues(parent, stackFS, root, values)
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

func recoveryScript(composeFile string) string {
	return `#!/bin/sh
set -eu

# Generated by Dockman. This script is deliberately self-contained: recovery
# needs only Docker Compose, SOPS, the encrypted source and the age identity.
: "${SOPS_AGE_KEY_FILE:?set SOPS_AGE_KEY_FILE to the backed-up age identity}"
command -v sops >/dev/null 2>&1 || { echo "sops is required" >&2; exit 127; }
command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 127; }

cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
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
