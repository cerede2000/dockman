package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/RA341/dockman/internal/host/filesystem"
)

const (
	SOPSSourceFile     = "secrets.sops.yaml"
	maxSOPSSourceBytes = 4 << 20
	sopsTimeout        = 30 * time.Second
)

var ErrSOPSUnavailable = errors.New("SOPS/age is not configured")

type SOPSStatus struct {
	Available    bool   `json:"available"`
	SourcePath   string `json:"sourcePath"`
	SourceExists bool   `json:"sourceExists"`
	Recipient    string `json:"recipient,omitempty"`
	Issue        string `json:"issue,omitempty"`
}

type SOPSResult struct {
	SourcePath string   `json:"sourcePath"`
	Names      []string `json:"names"`
}

type materializationSnapshot struct {
	value  []byte
	exists bool
}

type SOPSRunner interface {
	Run(ctx context.Context, binary string, args []string, env []string, input []byte) ([]byte, error)
}

type execSOPSRunner struct{}

func (execSOPSRunner) Run(ctx context.Context, binary string, args []string, env []string, input []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = controlledSOPSEnvironment(env)
	command.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	var stderr strings.Builder
	command.Stdout = &limitedWriter{writer: &stdout, remaining: maxSOPSSourceBytes}
	command.Stderr = &limitedWriter{writer: &stderr, remaining: 8 << 10}
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("SOPS operation failed: %s", message)
	}
	return stdout.Bytes(), nil
}

func controlledSOPSEnvironment(overrides []string) []string {
	blocked := []string{"SOPS_AGE_KEY=", "SOPS_AGE_KEY_CMD=", "SOPS_AGE_KEY_FILE="}
	environment := make([]string, 0, len(os.Environ())+len(overrides)+1)
	for _, entry := range os.Environ() {
		drop := false
		for _, prefix := range blocked {
			if strings.HasPrefix(entry, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "SOPS_DISABLE_VERSION_CHECK=1")
	return append(environment, overrides...)
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(value []byte) (int, error) {
	original := len(value)
	if w.remaining <= 0 {
		return original, nil
	}
	if len(value) > w.remaining {
		value = value[:w.remaining]
	}
	written, err := w.writer.Write(value)
	w.remaining -= written
	if err != nil {
		return written, err
	}
	return original, nil
}

type SOPSProvider struct {
	runtime   Store
	resolve   FileSystemProvider
	binary    string
	keyFile   string
	recipient string
	runner    SOPSRunner
	operation sync.Mutex
}

func NewSOPSProvider(runtime Store, resolve FileSystemProvider, binary, keyFile, recipient string) *SOPSProvider {
	return &SOPSProvider{
		runtime: runtime, resolve: resolve, binary: strings.TrimSpace(binary),
		keyFile: strings.TrimSpace(keyFile), recipient: strings.TrimSpace(recipient), runner: execSOPSRunner{},
	}
}

func (p *SOPSProvider) Status(host, stackPath string) (SOPSStatus, error) {
	status := SOPSStatus{SourcePath: SOPSSourceFile, Recipient: p.recipient}
	if p.binary == "" || p.keyFile == "" || p.recipient == "" {
		status.Issue = "configure DOCKMAN_SOPS_AGE_KEY_FILE and DOCKMAN_SOPS_AGE_RECIPIENT"
		return status, nil
	}
	if info, err := os.Stat(p.keyFile); err != nil || !info.Mode().IsRegular() {
		status.Issue = "the configured age identity file is unavailable"
		return status, nil
	}
	if _, err := exec.LookPath(p.binary); err != nil {
		status.Issue = "the SOPS executable is unavailable"
		return status, nil
	}
	status.Available = true
	stackFS, root, err := p.resolveStack(host, stackPath)
	if err != nil {
		return status, err
	}
	info, err := stackFS.Lstat(stackFS.Join(root, SOPSSourceFile))
	if errors.Is(err, fs.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, fmt.Errorf("inspect encrypted secret source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxSOPSSourceBytes {
		return status, errors.New("encrypted secret source is not a bounded regular file")
	}
	status.SourceExists = true
	return status, nil
}

func (p *SOPSProvider) Export(parent context.Context, host, stackPath string) (SOPSResult, error) {
	p.operation.Lock()
	defer p.operation.Unlock()
	if err := p.requireAvailable(); err != nil {
		return SOPSResult{}, err
	}
	items, err := p.runtime.List(host, stackPath)
	if err != nil {
		return SOPSResult{}, err
	}
	if len(items) == 0 {
		return SOPSResult{}, errors.New("no runtime secret is available to encrypt")
	}
	values := make(map[string]string, len(items))
	names := make([]string, 0, len(items))
	decrypted := make([][]byte, 0, len(items))
	defer func() {
		for _, value := range decrypted {
			clear(value)
		}
	}()
	for _, item := range items {
		value, readErr := p.runtime.Read(host, stackPath, item.Name)
		if readErr != nil {
			return SOPSResult{}, readErr
		}
		decrypted = append(decrypted, value)
		if !utf8.Valid(value) {
			return SOPSResult{}, fmt.Errorf("runtime secret %q is binary; SOPS YAML export accepts UTF-8 values only", item.Name)
		}
		values[item.Name] = string(value)
		names = append(names, item.Name)
	}
	sort.Strings(names)
	plain, err := json.Marshal(values)
	if err != nil {
		return SOPSResult{}, errors.New("encode secrets for SOPS")
	}
	defer clear(plain)
	ctx, cancel := context.WithTimeout(parent, sopsTimeout)
	defer cancel()
	encrypted, err := p.runner.Run(ctx, p.binary, []string{
		"encrypt", "--age", p.recipient, "--input-type", "json", "--output-type", "yaml",
		"--filename-override", SOPSSourceFile, "/dev/stdin",
	}, p.environment(), plain)
	if err != nil {
		return SOPSResult{}, err
	}
	defer clear(encrypted)
	// Verify the ciphertext with the configured identity before replacing the source.
	verified, err := p.decrypt(ctx, encrypted)
	if err != nil {
		return SOPSResult{}, fmt.Errorf("verify encrypted secret source: %w", err)
	}
	for name, expected := range values {
		if actual, ok := verified[name]; !ok || actual != expected {
			return SOPSResult{}, errors.New("verify encrypted secret source: decrypted values do not match")
		}
	}
	stackFS, root, err := p.resolveStack(host, stackPath)
	if err != nil {
		return SOPSResult{}, err
	}
	if err = writeAtomic(stackFS, stackFS.Join(root, SOPSSourceFile), encrypted, 0o644); err != nil {
		return SOPSResult{}, err
	}
	return SOPSResult{SourcePath: SOPSSourceFile, Names: names}, nil
}

func (p *SOPSProvider) Materialize(parent context.Context, host, stackPath string) (SOPSResult, error) {
	p.operation.Lock()
	defer p.operation.Unlock()
	if err := p.requireAvailable(); err != nil {
		return SOPSResult{}, err
	}
	stackFS, root, err := p.resolveStack(host, stackPath)
	if err != nil {
		return SOPSResult{}, err
	}
	source := stackFS.Join(root, SOPSSourceFile)
	info, err := stackFS.Lstat(source)
	if err != nil {
		return SOPSResult{}, fmt.Errorf("inspect encrypted secret source: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxSOPSSourceBytes {
		return SOPSResult{}, errors.New("encrypted secret source is not a bounded regular file")
	}
	encrypted, err := stackFS.ReadFile(source)
	if err != nil {
		return SOPSResult{}, fmt.Errorf("read encrypted secret source: %w", err)
	}
	defer clear(encrypted)
	ctx, cancel := context.WithTimeout(parent, sopsTimeout)
	defer cancel()
	values, err := p.decrypt(ctx, encrypted)
	if err != nil {
		return SOPSResult{}, err
	}
	names := make([]string, 0, len(values))
	for name, value := range values {
		if !validSecretName(name) {
			return SOPSResult{}, fmt.Errorf("encrypted source contains invalid secret name %q", name)
		}
		if len(value) > MaxSecretBytes {
			return SOPSResult{}, fmt.Errorf("encrypted secret %q exceeds the 1 MiB limit", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	previous := make(map[string]materializationSnapshot, len(names))
	defer func() {
		for _, item := range previous {
			clear(item.value)
		}
	}()
	for _, name := range names {
		current, readErr := p.runtime.Read(host, stackPath, name)
		switch {
		case readErr == nil:
			previous[name] = materializationSnapshot{value: current, exists: true}
		case errors.Is(readErr, fs.ErrNotExist):
			previous[name] = materializationSnapshot{}
		default:
			return SOPSResult{}, fmt.Errorf("snapshot runtime secret %q: %w", name, readErr)
		}
	}
	// Values absent from the encrypted source are deliberately preserved.
	applied := make([]string, 0, len(names))
	for _, name := range names {
		value := []byte(values[name])
		if _, err = p.runtime.Write(host, stackPath, name, value); err != nil {
			clear(value)
			rollbackErr := p.rollbackMaterialization(host, stackPath, applied, previous)
			if rollbackErr != nil {
				return SOPSResult{}, fmt.Errorf("materialize encrypted secret %q: %w; rollback incomplete: %v", name, err, rollbackErr)
			}
			return SOPSResult{}, fmt.Errorf("materialize encrypted secret %q: %w; previous runtime values restored", name, err)
		}
		clear(value)
		applied = append(applied, name)
	}
	return SOPSResult{SourcePath: SOPSSourceFile, Names: names}, nil
}

func (p *SOPSProvider) rollbackMaterialization(host, stackPath string, applied []string, previous map[string]materializationSnapshot) error {
	for index := len(applied) - 1; index >= 0; index-- {
		name := applied[index]
		item := previous[name]
		if item.exists {
			if _, err := p.runtime.Write(host, stackPath, name, item.value); err != nil {
				return err
			}
			continue
		}
		if err := p.runtime.Delete(host, stackPath, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (p *SOPSProvider) decrypt(ctx context.Context, encrypted []byte) (map[string]string, error) {
	plain, err := p.runner.Run(ctx, p.binary, []string{
		"decrypt", "--input-type", "yaml", "--output-type", "json", "/dev/stdin",
	}, p.environment(), encrypted)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	values := map[string]string{}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&values); err != nil {
		return nil, errors.New("decrypted SOPS source must be a flat string map")
	}
	return values, nil
}

func (p *SOPSProvider) requireAvailable() error {
	if p.binary == "" || p.keyFile == "" || p.recipient == "" {
		return ErrSOPSUnavailable
	}
	if info, err := os.Stat(p.keyFile); err != nil || !info.Mode().IsRegular() {
		return errors.New("configured age identity file is unavailable")
	}
	return nil
}

func (p *SOPSProvider) environment() []string {
	return []string{"SOPS_AGE_KEY_FILE=" + p.keyFile}
}

func (p *SOPSProvider) resolveStack(host, stackPath string) (filesystem.FileSystem, string, error) {
	stackFS, root, err := p.resolve(strings.TrimSpace(host), strings.TrimSpace(stackPath))
	if err != nil {
		return nil, "", fmt.Errorf("resolve stack: %w", err)
	}
	info, err := stackFS.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, "", ErrInvalidStackPath
	}
	return stackFS, root, nil
}

func writeAtomic(stackFS filesystem.FileSystem, destination string, value []byte, mode os.FileMode) error {
	temporary, err := temporaryName()
	if err != nil {
		return err
	}
	temporaryPath := stackFS.Join(filepath.Dir(destination), temporary)
	file, err := stackFS.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary encrypted source: %w", err)
	}
	written := false
	defer func() {
		if !written {
			_ = stackFS.RemoveAll(temporaryPath)
		}
	}()
	if _, err = file.Write(value); err != nil {
		_ = file.Close()
		return fmt.Errorf("write encrypted source: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("close encrypted source: %w", err)
	}
	if err = stackFS.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("atomically replace encrypted source: %w", err)
	}
	written = true
	if err = stackFS.Chmod(destination, mode); err != nil {
		return fmt.Errorf("set encrypted source permissions: %w", err)
	}
	return nil
}
