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
	"strconv"
	"strings"
)

const (
	HostRuntimeConfigPath = "/etc/dockman-secrets-host.json"
	HostRuntimeBinaryPath = "/usr/local/libexec/dockman-secrets-host"
	HostRuntimeSOPSPath   = "/usr/local/libexec/dockman-sops"
	HostRuntimeUnitName   = "dockman-secrets-host.service"
	HostReconcileUnitName = "dockman-secrets-reconcile.service"
	HostReconcilePathName = "dockman-secrets-reconcile.path"
	// HostRuntimeReconcileRequestFile contains only a timestamp. Writing it
	// asks the host-side systemd.path unit to run the fixed materializer; it
	// never carries a secret or an arbitrary command.
	HostRuntimeReconcileRequestFile = ".dockman-secrets-reconcile"
	HostRuntimeMarkerFile           = ".dockman-runtime-tmpfs"
)

// HostRuntimeConfig is intentionally independent from Dockman's database.
// The host helper and systemd can reconstruct volatile plaintext before Docker
// starts using only this file, the age identity and the encrypted stack files.
type HostRuntimeConfig struct {
	StackRoot    string `json:"stackRoot"`
	AgeKeyFile   string `json:"ageKeyFile"`
	SOPSBinary   string `json:"sopsBinary"`
	TmpfsSizeMiB int    `json:"tmpfsSizeMiB"`
	FileMode     uint32 `json:"fileMode"`
}

type HostRuntimeResult struct {
	Stacks  int `json:"stacks"`
	Secrets int `json:"secrets"`
}

func (c *HostRuntimeConfig) normalize() error {
	c.StackRoot = filepath.Clean(strings.TrimSpace(c.StackRoot))
	c.AgeKeyFile = filepath.Clean(strings.TrimSpace(c.AgeKeyFile))
	c.SOPSBinary = filepath.Clean(strings.TrimSpace(c.SOPSBinary))
	if c.TmpfsSizeMiB == 0 {
		c.TmpfsSizeMiB = 16
	}
	if c.FileMode == 0 {
		c.FileMode = 0o444
	}
	if !filepath.IsAbs(c.StackRoot) || c.StackRoot == string(filepath.Separator) {
		return errors.New("stack root must be an absolute non-root directory")
	}
	if strings.ContainsAny(c.StackRoot, "\r\n") {
		return errors.New("stack root cannot contain line breaks")
	}
	if !filepath.IsAbs(c.AgeKeyFile) || !filepath.IsAbs(c.SOPSBinary) {
		return errors.New("age identity and SOPS paths must be absolute")
	}
	if c.TmpfsSizeMiB < 1 || c.TmpfsSizeMiB > 1024 {
		return errors.New("tmpfs size must be between 1 and 1024 MiB per stack")
	}
	if c.FileMode != 0o400 && c.FileMode != 0o440 && c.FileMode != 0o444 {
		return errors.New("runtime file mode must be 0400, 0440 or 0444")
	}
	return nil
}

func LoadHostRuntimeConfig(path string) (HostRuntimeConfig, error) {
	var config HostRuntimeConfig
	info, err := os.Lstat(path)
	if err != nil {
		return config, fmt.Errorf("inspect host runtime configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 64<<10 {
		return config, errors.New("host runtime configuration must be a bounded regular file")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("read host runtime configuration: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode host runtime configuration: %w", err)
	}
	return config, config.normalize()
}

// MaterializeHostRuntime performs bounded, one-shot work. It has no watcher,
// timer, cache or background process and therefore no idle CPU/RAM overhead.
func MaterializeHostRuntime(parent context.Context, config HostRuntimeConfig) (HostRuntimeResult, error) {
	if err := config.normalize(); err != nil {
		return HostRuntimeResult{}, err
	}
	if os.Geteuid() != 0 {
		return HostRuntimeResult{}, errors.New("host secret materialization must run as root")
	}
	if err := validateHostRuntimeInputs(config); err != nil {
		return HostRuntimeResult{}, err
	}
	markers, err := discoverEncryptedStacks(config.StackRoot)
	if err != nil {
		return HostRuntimeResult{}, err
	}
	desired := make(map[string]struct{}, len(markers))
	for _, stackRoot := range markers {
		desired[filepath.Join(stackRoot, RuntimeDirectory)] = struct{}{}
	}
	if err = cleanupRuntimeMounts(config.StackRoot, desired); err != nil {
		return HostRuntimeResult{}, err
	}
	// One malformed stack must not deprive every other stack of its secrets at
	// boot. Returning on the first error did exactly that, which contradicts
	// the Wants= coupling on the Docker unit: the whole point of letting the
	// daemon start after a failed materialization is that the damage stays
	// local to the stacks actually affected. The exit status still reflects the
	// failure, so systemctl status and the logs stay honest.
	result := HostRuntimeResult{}
	var failures []error
	for _, stackRoot := range markers {
		count, materializeErr := materializeEncryptedStack(parent, config, stackRoot)
		if materializeErr != nil {
			failures = append(failures, fmt.Errorf("materialize %s: %w", stackRoot, materializeErr))
			continue
		}
		result.Stacks++
		result.Secrets += count
	}
	return result, errors.Join(failures...)
}

func CleanupHostRuntime(config HostRuntimeConfig) error {
	if err := config.normalize(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("host secret cleanup must run as root")
	}
	return cleanupRuntimeMounts(config.StackRoot, map[string]struct{}{})
}

func validateHostRuntimeInputs(config HostRuntimeConfig) error {
	rootInfo, err := os.Stat(config.StackRoot)
	if err != nil || !rootInfo.IsDir() {
		return errors.New("configured stack root is unavailable")
	}
	keyInfo, err := os.Lstat(config.AgeKeyFile)
	if err != nil || !keyInfo.Mode().IsRegular() {
		return errors.New("configured age identity is unavailable or not a regular file")
	}
	if keyInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("age identity permissions are too open; use chmod 0600")
	}
	sopsInfo, err := os.Lstat(config.SOPSBinary)
	if err != nil || !sopsInfo.Mode().IsRegular() || sopsInfo.Mode().Perm()&0o111 == 0 {
		return errors.New("configured SOPS binary is unavailable or not executable")
	}
	return nil
}

// This walk runs at boot, before the Docker daemon. A stack root that happens
// to contain a deep or enormous tree — a media library, a restored backup —
// must not be able to hold the host hostage, so the traversal is bounded in
// both depth and entry count.
//
// The depth deliberately matches maxStackDiscoveryDepth used by ListStacks: a
// stack the user can reach and encrypt from the interface must be a stack the
// host runtime mounts at boot, otherwise it would come up with no secrets and
// no explanation.
const (
	maxHostDiscoveryDepth   = maxStackDiscoveryDepth
	maxHostDiscoveryEntries = 200_000
)

func discoverEncryptedStacks(stackRoot string) ([]string, error) {
	stacks := []string{}
	visited := 0
	err := filepath.WalkDir(stackRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		visited++
		if visited > maxHostDiscoveryEntries {
			return fmt.Errorf("stack root holds more than %d entries; narrow the configured root", maxHostDiscoveryEntries)
		}
		if entry.IsDir() {
			base := entry.Name()
			if current != stackRoot && (base == ".git" || base == RuntimeDirectory || base == ".dockman-backups" || base == "node_modules") {
				return filepath.SkipDir
			}
			if relative, relErr := filepath.Rel(stackRoot, current); relErr == nil && relative != "." {
				if len(strings.Split(relative, string(filepath.Separator))) >= maxHostDiscoveryDepth {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if entry.Name() != SOPSInlineMarkerFile {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("encrypted runtime marker cannot be a symlink")
		}
		stack := filepath.Dir(current)
		relative, err := filepath.Rel(stackRoot, stack)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("encrypted stack is outside the configured root")
		}
		marker, err := os.ReadFile(current)
		if err != nil || strings.TrimSpace(string(marker)) != "version=1" {
			return fmt.Errorf("%s has an invalid encrypted runtime marker", relative)
		}
		sourceInfo, err := os.Lstat(filepath.Join(stack, SOPSSourceFile))
		if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Size() <= 0 || sourceInfo.Size() > maxSOPSSourceBytes {
			return fmt.Errorf("%s has no valid bounded %s", relative, SOPSSourceFile)
		}
		stacks = append(stacks, stack)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover encrypted stacks: %w", err)
	}
	sort.Strings(stacks)
	return stacks, nil
}

func materializeEncryptedStack(parent context.Context, config HostRuntimeConfig, stackRoot string) (int, error) {
	runtimeDirectory := filepath.Join(stackRoot, RuntimeDirectory)
	newlyMounted, err := ensureRuntimeTmpfs(runtimeDirectory, config.TmpfsSizeMiB)
	if err != nil {
		return 0, err
	}
	completed := false
	defer func() {
		if !completed && newlyMounted {
			_ = exec.Command("umount", runtimeDirectory).Run()
			_ = os.Remove(runtimeDirectory)
		}
	}()
	if err := writeLocalAtomic(runtimeDirectory, HostRuntimeMarkerFile, []byte("version=1\n"), 0o400); err != nil {
		return 0, fmt.Errorf("mark volatile runtime: %w", err)
	}
	encrypted, err := readBoundedLocalFile(filepath.Join(stackRoot, SOPSSourceFile), maxSOPSSourceBytes)
	if err != nil {
		return 0, fmt.Errorf("read encrypted source: %w", err)
	}
	defer clear(encrypted)
	ctx, cancel := context.WithTimeout(parent, sopsTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, config.SOPSBinary, "decrypt", "--input-type", "yaml", "--output-type", "json", "/dev/stdin")
	command.Env = controlledSOPSEnvironment([]string{"SOPS_AGE_KEY_FILE=" + config.AgeKeyFile})
	command.Stdin = bytes.NewReader(encrypted)
	var stdout bytes.Buffer
	var stderr strings.Builder
	command.Stdout = &limitedWriter{writer: &stdout, remaining: maxSOPSSourceBytes}
	command.Stderr = &limitedWriter{writer: &stderr, remaining: 8 << 10}
	if err = command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return 0, fmt.Errorf("SOPS decryption failed: %s", message)
	}
	plain := stdout.Bytes()
	defer clear(plain)
	values := map[string]string{}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&values); err != nil {
		return 0, errors.New("decrypted SOPS source must be a flat string map")
	}
	for name, value := range values {
		if !validSecretName(name) || len(value) > MaxSecretBytes || strings.IndexByte(value, 0) >= 0 {
			return 0, fmt.Errorf("decrypted secret %q is invalid", name)
		}
	}
	entries, err := os.ReadDir(runtimeDirectory)
	if err != nil {
		return 0, fmt.Errorf("list volatile runtime directory: %w", err)
	}
	for _, entry := range entries {
		if _, keep := values[entry.Name()]; keep {
			continue
		}
		if entry.Type().IsRegular() && validSecretName(entry.Name()) {
			if err = os.Remove(filepath.Join(runtimeDirectory, entry.Name())); err != nil {
				return 0, fmt.Errorf("remove stale volatile secret %q: %w", entry.Name(), err)
			}
		}
	}
	names := sortedValueNames(values)
	for _, name := range names {
		value := []byte(values[name])
		if err = writeLocalAtomic(runtimeDirectory, name, value, os.FileMode(config.FileMode)); err != nil {
			clear(value)
			return 0, err
		}
		clear(value)
	}
	completed = true
	return len(names), nil
}

func readBoundedLocalFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return nil, errors.New("source is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > limit {
		clear(value)
		return nil, errors.New("source exceeds its size limit")
	}
	return value, nil
}

func ensureRuntimeTmpfs(directory string, sizeMiB int) (bool, error) {
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("runtime secret path must be a real directory")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("inspect runtime secret directory: %w", err)
	} else if err = os.Mkdir(directory, 0o700); err != nil {
		return false, fmt.Errorf("create runtime secret directory: %w", err)
	}
	mounted, err := pathIsMounted(directory)
	if err != nil {
		return false, err
	}
	if mounted {
		return false, os.Chmod(directory, 0o700)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, errors.New("refusing to cover a non-empty persistent .secrets directory with tmpfs")
	}
	options := "nodev,nosuid,noexec,mode=0700,size=" + strconv.Itoa(sizeMiB) + "m"
	command := exec.Command("mount", "-t", "tmpfs", "-o", options, "dockman-secrets", directory)
	if output, err := command.CombinedOutput(); err != nil {
		return false, fmt.Errorf("mount volatile secret tmpfs: %s", strings.TrimSpace(string(output)))
	}
	return true, nil
}

func pathIsMounted(path string) (bool, error) {
	value, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, fmt.Errorf("read mount table: %w", err)
	}
	clean := filepath.Clean(path)
	for _, line := range strings.Split(string(value), "\n") {
		fields := strings.Fields(line)
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if len(fields) <= 4 || separator < 0 || len(fields) <= separator+2 || filepath.Clean(unescapeMountInfoPath(fields[4])) != clean {
			continue
		}
		if fields[separator+1] == "tmpfs" && fields[separator+2] == "dockman-secrets" {
			return true, nil
		}
		return false, errors.New("runtime secret directory is occupied by an unmanaged mount")
	}
	return false, nil
}

// IsManagedHostRuntimeMount verifies the filesystem type and mount source, not
// merely the portable marker stored inside it. This prevents a forged marker
// on persistent storage from making Dockman write plaintext to disk.
func IsManagedHostRuntimeMount(path string) (bool, error) { return pathIsMounted(path) }

func managedRuntimeMounts(stackRoot string) ([]string, error) {
	value, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("read mount table: %w", err)
	}
	root := filepath.Clean(stackRoot)
	mounts := []string{}
	for _, line := range strings.Split(string(value), "\n") {
		fields := strings.Fields(line)
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if len(fields) <= 4 || separator < 0 || len(fields) <= separator+2 {
			continue
		}
		mount := filepath.Clean(unescapeMountInfoPath(fields[4]))
		if fields[separator+1] != "tmpfs" || fields[separator+2] != "dockman-secrets" {
			continue
		}
		if root != "/" && mount != root && !strings.HasPrefix(mount, root+string(filepath.Separator)) {
			continue
		}
		mounts = append(mounts, mount)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(mounts)))
	return mounts, nil
}

func cleanupRuntimeMounts(stackRoot string, desired map[string]struct{}) error {
	mounts, err := managedRuntimeMounts(stackRoot)
	if err != nil {
		return err
	}
	for _, mount := range mounts {
		if _, keep := desired[mount]; keep {
			continue
		}
		if output, unmountErr := exec.Command("umount", mount).CombinedOutput(); unmountErr != nil {
			return fmt.Errorf("unmount obsolete volatile secret runtime %s: %s", mount, strings.TrimSpace(string(output)))
		}
		if removeErr := os.Remove(mount); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return fmt.Errorf("remove obsolete volatile secret directory %s: %w", mount, removeErr)
		}
	}
	return nil
}

func unescapeMountInfoPath(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}

func writeLocalAtomic(directory, name string, value []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(directory, ".dockman-secret-*")
	if err != nil {
		return fmt.Errorf("create volatile secret %q: %w", name, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(value)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write volatile secret %q: %w", name, err)
	}
	if err = os.Chmod(temporaryPath, mode); err != nil {
		return fmt.Errorf("set volatile secret %q permissions: %w", name, err)
	}
	if err = os.Rename(temporaryPath, filepath.Join(directory, name)); err != nil {
		return fmt.Errorf("replace volatile secret %q: %w", name, err)
	}
	return nil
}
